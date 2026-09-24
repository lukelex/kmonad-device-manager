package manager

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func runService(ctx context.Context, jsonOutput bool, buildVersion string) int {
	if jsonOutput {
		_ = os.Setenv("KMONAD_LOG_FORMAT", "json")
	}
	if !host.Supported() {
		writeCLIError(os.Stderr, jsonOutput, "unsupported_platform", "KMonad Device Manager requires Linux with the evdev backend")
		return 2
	}
	s := loadSettings()
	if err := validateSettings(s); err != nil {
		writeCLIError(os.Stderr, jsonOutput, "invalid_settings", err.Error())
		return 2
	}
	if !host.KMonadAvailable(s.kmonadCommand) {
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
		devices:  make(map[string]Device), deviceRegistryPath: filepath.Join(filepath.Dir(statusPath), "devices.json"),
		operations:       make(map[string]Operation),
		managedConfigs:   make(map[string]managedConfiguration),
		prevalidated:     make(map[string]*validatedConfig),
		managedTampered:  make(map[string]bool),
		externalConfigs:  make(map[string]externalConfiguration),
		eventSubscribers: make(map[uint64]*eventSubscriber),
	}
	m.loadDeviceRegistry()
	m.probeKMonadVersion(ctx)
	if base, stateErr := stateDir(); stateErr != nil {
		logf("managed configuration storage unavailable: %v", stateErr)
	} else if stateErr := m.openManagedConfigurationStore(base); stateErr != nil {
		logf("managed configuration storage unavailable: %v", stateErr)
	} else {
		m.loadExternalConfigurationRegistry()
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
