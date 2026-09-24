// Package manager owns the KMonad supervision service and its command-facing
// domain operations. The command package only supplies process bootstrap and
// build metadata.
package manager

import (
	"context"
	"errors"
	"io"
	"os"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
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
	child          platform.ChildProcess
	pid            int
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
	configDir             string
	managedConfigDir      string
	kmonadCommand         string
	kmonad                KMonadInfo
	nextKMonadCheck       time.Time
	stopTimeout           time.Duration
	dryRunTimeout         time.Duration
	watchdogTimeout       time.Duration
	cgroupRoot            string
	processMemoryMax      string
	processCPUQuota       string
	statusPath            string
	maxConfigs            int
	maxConfigBytes        int64
	watchPaths            map[string]bool
	states                map[string]*configState
	duplicates            map[string]string
	commands              chan managerCommand
	devices               map[string]Device
	discoveredNodeDevices map[string]Device
	deviceRegistryPath    string
	managedConfigs        map[string]managedConfiguration
	managedTampered       map[string]bool
	externalConfigs       map[string]externalConfiguration
	externalRegistryPath  string
	events                []Event
	eventStart            int
	nextEventID           uint64
	nextSubscriberID      uint64
	eventSubscribers      map[uint64]*eventSubscriber
	prevalidated          map[string]*validatedConfig
	operations            map[string]Operation
	idempotencyPath       string
	idempotencyRecords    map[string]idempotencyRecord
	identification        *identificationSession
	probe                 *probeSession
	inputScanBasis        map[string]string
	inputScanGeneration   map[string]int
	runContext            context.Context
	stateRevision         uint64
	lastPublicState       publicState
	publicStateRevision   atomic.Uint64
	lastProgress          atomic.Int64
	metricsServerUp       atomic.Bool
	metricsFailures       atomic.Uint64
	statusFailures        atomic.Uint64
	reconciles            atomic.Uint64
	starts                atomic.Uint64
	failures              atomic.Uint64
	stops                 atomic.Uint64
	publicEvents          [publicEventMetricCount]atomic.Uint64
}

type statusFile struct {
	PID            int            `json:"pid"`
	ProcessStart   uint64         `json:"process_start"`
	UpdatedAt      time.Time      `json:"updated_at"`
	ConfigDir      string         `json:"config_dir"`
	Configurations []statusConfig `json:"configurations"`
}

type statusConfig struct {
	Name                   string             `json:"name"`
	State                  string             `json:"state"`
	Device                 string             `json:"device,omitempty"`
	ProcessID              int                `json:"process_id,omitempty"`
	ProcessStart           uint64             `json:"process_start,omitempty"`
	ProcessGroupID         int                `json:"process_group_id,omitempty"`
	LaunchPath             string             `json:"launch_path,omitempty"`
	Connected              bool               `json:"connected"`
	Availability           DeviceAvailability `json:"availability,omitempty"`
	AvailabilityReasonCode ReasonCode         `json:"availability_reason_code,omitempty"`
	ReasonCode             ReasonCode         `json:"reason_code,omitempty"`
	Healthy                bool               `json:"healthy"`
	Reason                 string             `json:"reason,omitempty"`
	Failures               int                `json:"failures,omitempty"`
	RetryAfter             time.Time          `json:"retry_after,omitempty"`
	FailureReason          string             `json:"failure_reason,omitempty"`
	LastKnownGoodSignature string             `json:"last_known_good_signature,omitempty"`
}

var (
	errLockHeld                = platform.ErrLockHeld
	errConfigChanged           = errors.New("configuration changed while it was being read")
	logOutput        io.Writer = os.Stderr
	newWatcher                 = fsnotify.NewWatcher
	host                       = platform.Default()
)
