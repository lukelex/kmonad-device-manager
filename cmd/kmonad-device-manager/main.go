package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/lukelex/kmonad-device-manager/internal/completions"
)

var version = "dev"

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
	pidfd          *os.File
	done           chan struct{}
	exitErr        error
	startedAt      time.Time
	startTick      uint64
	unhealthySince time.Time
	cgroupPath     string
}

type configState struct {
	phase            configPhase
	signature        string
	deviceID         string
	failures         int
	retryAfter       time.Time
	process          *processState
	pendingSignature string
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
	Name         string    `json:"name"`
	State        string    `json:"state"`
	Device       string    `json:"device,omitempty"`
	ProcessID    int       `json:"process_id,omitempty"`
	ProcessStart uint64    `json:"process_start,omitempty"`
	Connected    bool      `json:"connected"`
	Healthy      bool      `json:"healthy"`
	Reason       string    `json:"reason,omitempty"`
	Failures     int       `json:"failures,omitempty"`
	RetryAfter   time.Time `json:"retry_after,omitempty"`
}

var (
	errLockHeld                = errors.New("another manager instance is already running")
	errConfigChanged           = errors.New("configuration changed while it was being read")
	logOutput        io.Writer = os.Stderr
	newWatcher                 = fsnotify.NewWatcher
)

func main() {
	s := loadSettings()

	switch {
	case len(os.Args) == 2 && os.Args[1] == "--doctor":
		os.Exit(doctor(s))
	case len(os.Args) == 2 && os.Args[1] == "--version":
		fmt.Printf("kmonad-device-manager %s\n", version)
		return
	case len(os.Args) == 2 && (os.Args[1] == "--status" || os.Args[1] == "ps"):
		os.Exit(showStatus(false))
	case len(os.Args) == 2 && os.Args[1] == "--status=json":
		os.Exit(showStatus(true))
	case len(os.Args) == 2 && (os.Args[1] == "-h" || os.Args[1] == "--help"):
		fmt.Println("Usage: kmonad-device-manager [--doctor] [--status|--status=json|ps] [--completion <bash|zsh|fish>] [--version]")
		return
	case len(os.Args) == 3 && os.Args[1] == "--completion":
		output, err := completions.For(os.Args[2])
		if err != nil {
			fmt.Fprintf(os.Stderr, "kmonad-device-manager: %v\n", err)
			os.Exit(2)
		}
		fmt.Print(output)
		return
	case len(os.Args) == 1:
		// Continue into the service.
	default:
		fmt.Fprintf(os.Stderr, "kmonad-device-manager: unknown option: %s\n", os.Args[1])
		os.Exit(2)
	}

	if err := validateSettings(s); err != nil {
		fmt.Fprintf(os.Stderr, "kmonad-device-manager: %v\n", err)
		os.Exit(2)
	}
	if _, err := exec.LookPath(s.kmonadCommand); err != nil {
		fmt.Fprintln(os.Stderr, "kmonad-device-manager: KMonad is not installed or is not on PATH.")
		fmt.Fprintln(os.Stderr, "Install KMonad from https://github.com/kmonad/kmonad, then restart this service.")
		os.Exit(127)
	}

	lock, lockPath, err := acquireLock()
	if err != nil {
		if errors.Is(err, errLockHeld) {
			fmt.Fprintln(os.Stderr, "kmonad-device-manager: another instance is already running.")
		} else {
			fmt.Fprintf(os.Stderr, "kmonad-device-manager: cannot acquire single-instance lock: %v\n", err)
		}
		os.Exit(1)
	}
	defer releaseLock(lock)

	statusPath := filepath.Join(filepath.Dir(lockPath), "status.json")
	previousStatus := readStatusFile(statusPath)
	recoverOwnedProcesses(statusPath, s.kmonadCommand)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	m := &manager{
		configDir:        s.configDir,
		kmonadCommand:    s.kmonadCommand,
		stopTimeout:      s.stopTimeout,
		dryRunTimeout:    s.dryRunTimeout,
		watchdogTimeout:  s.watchdogTimeout,
		cgroupRoot:       s.cgroupRoot,
		processMemoryMax: s.processMemoryMax,
		processCPUQuota:  s.processCPUQuota,
		maxConfigs:       s.maxConfigs,
		maxConfigBytes:   s.maxConfigBytes,
		watchPaths:       make(map[string]bool),
		statusPath:       statusPath,
		states:           make(map[string]*configState),
		duplicates:       make(map[string]string),
	}
	m.markProgress()
	m.restoreBackoff(previousStatus)
	defer m.cleanup()
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
}
