// Package manager owns the KMonad supervision service and its command-facing
// domain operations. The command package only supplies process bootstrap and
// build metadata.
package manager

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/lukelex/kmonad-device-manager/internal/completions"
	"github.com/lukelex/kmonad-device-manager/internal/platform"
)

type configToken struct {
	kind  byte
	value string
}

type settings struct {
	configDir          string
	kmonadCommand      string
	pollIntervalRaw    string
	stopTimeoutRaw     string
	dryRunTimeoutRaw   string
	maxConfigsRaw      string
	maxConfigBytesRaw  string
	watchdogTimeoutRaw string
	metricsAddr        string
	metricsAllowRemote bool
	cgroupRoot         string
	processMemoryMax   string
	processCPUQuota    string
	pollInterval       time.Duration
	stopTimeout        time.Duration
	dryRunTimeout      time.Duration
	watchdogTimeout    time.Duration
	maxConfigs         int
	maxConfigBytes     int64
}

type processState struct {
	cmd            *exec.Cmd
	pidfd          platform.ProcessHandle
	done           chan struct{}
	exitResult     chan error
	startedAt      time.Time
	startTick      uint64
	processGroupID int
	launchPath     string
	unhealthySince time.Time
	cgroupPath     string
}

type configState struct {
	phase                  configPhase
	signature              string
	deviceID               string
	failures               int
	retryAfter             time.Time
	process                *processState
	pendingSignature       string
	failureReason          string
	lastKnownGoodSignature string
}

type configPhase string

const (
	phaseDiscovered configPhase = "discovered"
	phaseValidating configPhase = "validating"
	phaseWaiting    configPhase = "waiting"
	phaseRunning    configPhase = "running"
	phaseFailed     configPhase = "failed"
	phaseDuplicate  configPhase = "duplicate"
	phaseStopped    configPhase = "stopped"
)

var validPhaseTransitions = map[configPhase]map[configPhase]bool{
	phaseDiscovered: {phaseValidating: true, phaseDuplicate: true, phaseStopped: true},
	phaseValidating: {phaseRunning: true, phaseFailed: true, phaseWaiting: true, phaseStopped: true},
	phaseWaiting:    {phaseValidating: true, phaseStopped: true},
	phaseRunning:    {phaseFailed: true, phaseStopped: true},
	phaseFailed:     {phaseWaiting: true, phaseValidating: true, phaseStopped: true},
	phaseDuplicate:  {phaseStopped: true, phaseValidating: true},
	phaseStopped:    {phaseValidating: true},
}

func transitionPhase(state *configState, next configPhase) bool {
	if state == nil || state.phase == next {
		return state != nil
	}
	if !validPhaseTransitions[state.phase][next] {
		return false
	}
	state.phase = next
	return true
}

// manager state is mutated only by the service's run/reconciliation goroutine.
// Worker goroutines wait for processes or serve atomic metric counters; they do
// not mutate configuration state.
type manager struct {
	configDir        string
	kmonadCommand    string
	stopTimeout      time.Duration
	dryRunTimeout    time.Duration
	watchdogTimeout  time.Duration
	cgroupRoot       string
	processMemoryMax string
	processCPUQuota  string
	statusPath       string
	maxConfigs       int
	maxConfigBytes   int64
	watchPaths       map[string]bool
	states           map[string]*configState
	duplicates       map[string]string
	commands         chan managerCommand
	lastProgress     atomic.Int64
	metricsServerUp  atomic.Bool
	metricsFailures  atomic.Uint64
	statusFailures   atomic.Uint64
	reconciles       atomic.Uint64
	starts           atomic.Uint64
	failures         atomic.Uint64
	stops            atomic.Uint64
}

type statusFile struct {
	PID            int            `json:"pid"`
	ProcessStart   uint64         `json:"process_start"`
	UpdatedAt      time.Time      `json:"updated_at"`
	ConfigDir      string         `json:"config_dir"`
	Configurations []statusConfig `json:"configurations"`
}

type statusConfig struct {
	Name                   string    `json:"name"`
	State                  string    `json:"state"`
	Device                 string    `json:"device,omitempty"`
	ProcessID              int       `json:"process_id,omitempty"`
	ProcessStart           uint64    `json:"process_start,omitempty"`
	ProcessGroupID         int       `json:"process_group_id,omitempty"`
	LaunchPath             string    `json:"launch_path,omitempty"`
	Connected              bool      `json:"connected"`
	Healthy                bool      `json:"healthy"`
	Reason                 string    `json:"reason,omitempty"`
	Failures               int       `json:"failures,omitempty"`
	RetryAfter             time.Time `json:"retry_after,omitempty"`
	FailureReason          string    `json:"failure_reason,omitempty"`
	LastKnownGoodSignature string    `json:"last_known_good_signature,omitempty"`
}

var (
	errLockHeld                = platform.ErrLockHeld
	errConfigChanged           = errors.New("configuration changed while it was being read")
	logOutput        io.Writer = os.Stderr
	newWatcher                 = fsnotify.NewWatcher
	host                       = platform.Default()
)

// Run executes a complete command invocation and returns its documented exit
// status. It does not call os.Exit, so callers remain responsible for process
// bootstrap and termination.
func Run(ctx context.Context, arguments []string, buildVersion string) int {
	invocation, err := parseCLIInvocation(arguments)
	if err != nil {
		writeCLIError(os.Stderr, containsJSONOption(arguments), "invalid_arguments", err.Error())
		return 2
	}

	switch {
	case len(invocation.args) == 1 && invocation.args[0] == "--doctor":
		return doctor(loadSettings(), invocation.jsonOutput)
	case len(invocation.args) == 1 && invocation.args[0] == "--version":
		if err := writeVersionFor(os.Stdout, invocation.jsonOutput, buildVersion); err != nil {
			writeCLIError(os.Stderr, invocation.jsonOutput, "output_failed", err.Error())
			return 1
		}
		return 0
	case len(invocation.args) == 1 && (invocation.args[0] == "--status" || invocation.args[0] == "ps"):
		return showStatus(invocation.jsonOutput)
	case len(invocation.args) == 1 && (invocation.args[0] == "-h" || invocation.args[0] == "--help"):
		if err := writeHelp(os.Stdout, invocation.jsonOutput); err != nil {
			writeCLIError(os.Stderr, invocation.jsonOutput, "output_failed", err.Error())
			return 1
		}
		return 0
	case len(invocation.args) == 2 && invocation.args[0] == "--completion":
		output, err := completions.For(invocation.args[1])
		if err != nil {
			writeCLIError(os.Stderr, invocation.jsonOutput, "unsupported_shell", err.Error())
			return 2
		}
		if err := writeCompletion(os.Stdout, invocation.args[1], output, invocation.jsonOutput); err != nil {
			writeCLIError(os.Stderr, invocation.jsonOutput, "output_failed", err.Error())
			return 1
		}
		return 0
	case len(invocation.args) == 0:
		return runService(ctx, invocation.jsonOutput, buildVersion)
	default:
		message := "invalid command line"
		if len(invocation.args) > 0 {
			message = "unknown option or invalid arguments: " + invocation.args[0]
		}
		writeCLIError(os.Stderr, invocation.jsonOutput, "invalid_arguments", message)
		return 2
	}
}

func runService(ctx context.Context, jsonOutput bool, buildVersion string) int {
	if jsonOutput {
		_ = os.Setenv("KMONAD_LOG_FORMAT", "json")
	}
	s := loadSettings()
	if err := validateSettings(s); err != nil {
		writeCLIError(os.Stderr, jsonOutput, "invalid_settings", err.Error())
		return 2
	}
	if _, err := exec.LookPath(s.kmonadCommand); err != nil {
		writeCLIError(os.Stderr, jsonOutput, "kmonad_not_found", "KMonad is not installed or is not on PATH; install KMonad from https://github.com/kmonad/kmonad, then restart this service")
		return 127
	}
	lock, lockPath, err := acquireLock()
	if err != nil {
		if errors.Is(err, errLockHeld) {
			writeCLIError(os.Stderr, jsonOutput, "lock_held", "another instance is already running")
		} else {
			writeCLIError(os.Stderr, jsonOutput, "lock_failed", fmt.Sprintf("cannot acquire single-instance lock: %v", err))
		}
		return 1
	}
	defer releaseLock(lock)

	statusPath := filepath.Join(filepath.Dir(lockPath), "status.json")
	previousStatus := readStatusFile(statusPath)
	recoverOwnedProcesses(statusPath, s.kmonadCommand)
	m := &manager{
		configDir: s.configDir, kmonadCommand: s.kmonadCommand,
		stopTimeout: s.stopTimeout, dryRunTimeout: s.dryRunTimeout, watchdogTimeout: s.watchdogTimeout,
		cgroupRoot: s.cgroupRoot, processMemoryMax: s.processMemoryMax, processCPUQuota: s.processCPUQuota,
		maxConfigs: s.maxConfigs, maxConfigBytes: s.maxConfigBytes, watchPaths: make(map[string]bool),
		statusPath: statusPath, states: make(map[string]*configState), duplicates: make(map[string]string),
		commands: make(chan managerCommand, managerCommandQueueSize),
	}
	m.markProgress()
	m.restoreBackoff(previousStatus)
	defer m.cleanup()
	if apiSocketPath, err := host.APISocketPath(); err != nil {
		logf("API listener unavailable: %v", err)
	} else {
		go serveAPISocket(ctx, apiSocketPath, buildVersion, m)
	}
	systemdNotify("READY=1\nSTATUS=KMonad device manager is running")
	go systemdWatchdog(ctx, &m.lastProgress)
	if s.metricsAddr != "" {
		server, err := startMetricsServer(m, s.metricsAddr)
		if err != nil {
			logf("metrics server unavailable: %v", err)
		} else {
			defer server.Shutdown(context.Background())
		}
	}
	m.run(ctx, s.pollInterval)
	return 0
}

func containsJSONOption(arguments []string) bool {
	for _, argument := range arguments {
		if argument == "--json" || argument == "--status=json" {
			return true
		}
	}
	return false
}
