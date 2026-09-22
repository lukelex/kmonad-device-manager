package main

import (
	"errors"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type validatedConfig struct {
	device     string
	signature  string
	launchPath string
}

var retryJitter = func(max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	return time.Duration(rand.Int63n(int64(max) + 1))
}

var (
	cgroupMkdir     = os.Mkdir
	cgroupStat      = os.Stat
	cgroupWriteFile = os.WriteFile
)

func (m *manager) reconcile(now time.Time) {
	m.reconciles.Add(1)
	activeConfigs := make(map[string]bool)
	activeDevices := make(map[string]string)
	stopDeadline := now.Add(m.stopTimeout)
	configCount := 0
	if unsafe, err := worldWritable(m.configDir); err != nil && !os.IsNotExist(err) {
		logf("cannot inspect configuration directory: %v", err)
		m.stopAll(stopDeadline)
		m.writeStatus()
		return
	} else if unsafe {
		logf("configuration directory is writable by other users; refusing to run configurations from it")
		m.stopAll(stopDeadline)
		m.writeStatus()
		return
	}

	entries, err := os.ReadDir(m.configDir)
	if err != nil {
		if !os.IsNotExist(err) {
			logf("cannot read configuration directory: %v", err)
		}
		entries = nil
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".kbd") {
			continue
		}
		config := filepath.Join(m.configDir, entry.Name())
		activeConfigs[config] = true
		configCount++
		if configCount > m.maxConfigs {
			logConfigEvent("configuration_limit", config, "configuration limit reached", map[string]any{"limit": m.maxConfigs})
			m.stopAndDelete(config, stopDeadline)
			continue
		}
		if unsafe, err := worldWritable(config); err != nil {
			logf("cannot inspect %s; stopping it: %v", entry.Name(), err)
			m.stopAndDelete(config, stopDeadline)
			continue
		} else if unsafe {
			logf("configuration %s is writable by other users; refusing to run it", entry.Name())
			m.stopAndDelete(config, stopDeadline)
			continue
		}

		device, signature, err := readConfigWithLimit(config, m.maxConfigBytes)
		if err != nil {
			if errors.Is(err, errConfigChanged) {
				logf("configuration %s changed while it was being read; retrying", entry.Name())
				continue
			}
			logf("cannot read %s; stopping it: %v", entry.Name(), err)
			m.stopAndDelete(config, stopDeadline)
			continue
		}
		if !deviceReady(device) {
			m.stopAndDelete(config, stopDeadline)
			continue
		}
		identity, err := deviceID(device)
		if err != nil {
			logf("%s vanished while checking its device; stopping it", entry.Name())
			m.stopAndDelete(config, stopDeadline)
			continue
		}
		if primary, ok := activeDevices[identity]; ok {
			if state := m.states[config]; state != nil {
				transitionPhase(state, phaseDuplicate)
			}
			if m.duplicates[config] != identity {
				logConfigEvent("duplicate_configuration", config, "configuration uses a device already claimed by another configuration", map[string]any{"primary": filepath.Base(primary), "device_id": identity})
				m.duplicates[config] = identity
			}
			m.stopAndDelete(config, stopDeadline)
			continue
		}
		activeDevices[identity] = config
		delete(m.duplicates, config)

		state := m.states[config]
		if state == nil {
			state = &configState{phase: phaseDiscovered}
			m.states[config] = state
		}
		changed := state.deviceID != identity || state.signature != signature
		var validation *validatedConfig
		if changed && state.process != nil {
			if state.pendingSignature == signature && now.Before(state.retryAfter) {
				continue
			}
			validation, err = m.validateConfigSnapshot(config, signature)
			if err != nil {
				if errors.Is(err, errConfigChanged) {
					state.failureReason = "configuration changed during validation"
					logConfigEvent("configuration_changed_during_validation", config, "configuration changed while being validated; keeping the last known-good process", nil)
					continue
				}
				state.pendingSignature = signature
				m.scheduleRetry(config, state, now, "configuration update validation failed: "+err.Error())
				logConfigEvent("rollback_last_good", config, "new configuration failed validation; keeping the last known-good process", map[string]any{"error": err.Error()})
				continue
			}
		}
		if changed {
			m.stopProcess(config, state, stopDeadline)
			state.failures = 0
			state.retryAfter = time.Time{}
			state.deviceID = identity
			state.signature = signature
			state.pendingSignature = ""
			state.failureReason = ""
		}

		if state.process != nil {
			if !m.processHealthy(config, state.process) {
				if state.process.unhealthySince.IsZero() {
					state.process.unhealthySince = now
				} else if now.Sub(state.process.unhealthySince) >= m.watchdogTimeout {
					logConfigEvent("watchdog_timeout", config, "KMonad process failed the watchdog check", map[string]any{"pid": state.process.cmd.Process.Pid})
					m.stopProcess(config, state, stopDeadline)
					m.scheduleRetry(config, state, now, "watchdog timeout")
					continue
				}
			} else {
				state.process.unhealthySince = time.Time{}
			}
			select {
			case <-state.process.done:
				process := state.process
				if process.exitErr != nil {
					logConfigEvent("process_exited", config, "KMonad process exited", map[string]any{"error": process.exitErr.Error()})
				}
				signalProcessGroup(process, syscall.SIGKILL)
				if err := cleanupCgroup(process.cgroupPath); err != nil {
					logConfigEvent("cgroup_cleanup_failed", config, "failed to remove KMonad cgroup", map[string]any{"error": err.Error()})
				}
				closeProcessFD(process)
				cleanupLaunchSnapshot(config, process)
				state.process = nil
				reason := "process exited"
				if process.exitErr != nil {
					reason = "process exited: " + process.exitErr.Error()
				}
				m.scheduleRetry(config, state, now, reason)
			default:
				if now.Sub(state.process.startedAt) >= 30*time.Second {
					state.failures = 0
					state.retryAfter = time.Time{}
				}
				continue
			}
		}
		if now.Before(state.retryAfter) {
			transitionPhase(state, phaseWaiting)
			continue
		}
		m.startConfig(config, state, now, signature, validation)
	}

	for config := range m.states {
		if !activeConfigs[config] {
			m.stopAndDelete(config, stopDeadline)
		}
	}
	for config := range m.duplicates {
		if !activeConfigs[config] {
			delete(m.duplicates, config)
		}
	}
	m.writeStatus()
}

func (m *manager) stopAndDelete(config string, deadline time.Time) {
	if state := m.states[config]; state != nil {
		m.stopProcess(config, state, deadline)
		delete(m.states, config)
	}
}

func (m *manager) stopAll(deadline time.Time) {
	states := make(map[string]*configState, len(m.states))
	for config, state := range m.states {
		states[config] = state
		delete(m.states, config)
	}
	m.stopStates(states, deadline)
}

func (m *manager) stopStates(states map[string]*configState, deadline time.Time) {
	var waitGroup sync.WaitGroup
	waitGroup.Add(len(states))
	for config, state := range states {
		go func(config string, state *configState) {
			defer waitGroup.Done()
			m.stopProcess(config, state, deadline)
		}(config, state)
	}
	waitGroup.Wait()
}

func (m *manager) startConfig(config string, state *configState, now time.Time, expectedSignature string, validation *validatedConfig) {
	transitionPhase(state, phaseValidating)
	if validation == nil {
		var err error
		validation, err = m.validateConfigSnapshot(config, expectedSignature)
		if err != nil {
			if errors.Is(err, errConfigChanged) {
				state.failureReason = "configuration changed during validation"
				logConfigEvent("configuration_changed_during_validation", config, "configuration changed before it could be started", nil)
				state.retryAfter = now.Add(time.Second)
				transitionPhase(state, phaseWaiting)
				return
			}
			if os.IsNotExist(err) {
				state.failureReason = "configuration disappeared during validation"
				logConfigEvent("configuration_disappeared", config, "configuration disappeared during validation", nil)
				state.retryAfter = now.Add(time.Second)
				transitionPhase(state, phaseWaiting)
				return
			}
			m.failures.Add(1)
			transitionPhase(state, phaseFailed)
			logConfigEvent("validation_failed", config, "KMonad dry-run validation failed", nil)
			m.scheduleRetry(config, state, now, "validation failed: "+err.Error())
			return
		}
	}
	cleanupValidation := true
	defer func() {
		if cleanupValidation {
			_ = removeConfigSnapshot(validation.launchPath)
		}
	}()
	identity, identityErr := deviceID(validation.device)
	if !deviceReady(validation.device) || identityErr != nil || (state.deviceID != "" && identity != state.deviceID) {
		logConfigEvent("device_disappeared", config, "input device disappeared during startup", map[string]any{"device": validation.device})
		state.retryAfter = time.Time{}
		transitionPhase(state, phaseWaiting)
		return
	}

	cmd := exec.Command(m.kmonadCommand, validation.launchPath)
	cmd.Stdout, cmd.Stderr = childOutputWriters(config)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid:   true,
		Pdeathsig: syscall.SIGTERM,
	}
	if err := cmd.Start(); err != nil {
		m.failures.Add(1)
		logConfigEvent("process_start_failed", config, "failed to start KMonad", map[string]any{"error": err.Error()})
		m.scheduleRetry(config, state, now, "process start failed: "+err.Error())
		return
	}
	process := newProcessState(cmd)
	if err := m.attachProcessCgroup(config, cmd.Process.Pid); err != nil {
		logConfigEvent("cgroup_attach_failed", config, "failed to isolate KMonad process", map[string]any{"error": err.Error()})
		signalProcess(process, syscall.SIGKILL)
		_ = cmd.Wait()
		closeProcessFD(process)
		m.failures.Add(1)
		m.scheduleRetry(config, state, now, "cgroup attach failed: "+err.Error())
		return
	}
	process.done = make(chan struct{})
	process.startedAt = now
	if m.cgroupRoot != "" {
		process.cgroupPath = filepath.Join(m.cgroupRoot, filepath.Base(config))
	}
	process.launchPath = validation.launchPath
	state.process = process
	cleanupValidation = false
	state.lastKnownGoodSignature = expectedSignature
	m.starts.Add(1)
	transitionPhase(state, phaseRunning)
	logConfigEvent("process_started", config, "KMonad process started", map[string]any{"pid": cmd.Process.Pid})
	go func() {
		process.exitErr = cmd.Wait()
		close(process.done)
	}()
}

func (m *manager) validateConfigSnapshot(config, expectedSignature string) (*validatedConfig, error) {
	device, actualSignature, data, err := readConfigDataWithLimit(config, m.maxConfigBytes)
	if err != nil {
		return nil, err
	}
	if actualSignature != expectedSignature {
		return nil, errConfigChanged
	}
	launchPath, err := createConfigSnapshot(config, data)
	if err != nil {
		return nil, err
	}
	validation := &validatedConfig{device: device, signature: actualSignature, launchPath: launchPath}
	if err := m.dryRun(launchPath); err != nil {
		_ = removeConfigSnapshot(launchPath)
		return nil, err
	}
	_, finalSignature, err := readConfigWithLimit(config, m.maxConfigBytes)
	if err != nil {
		_ = removeConfigSnapshot(launchPath)
		return nil, err
	}
	if finalSignature != actualSignature {
		_ = removeConfigSnapshot(launchPath)
		return nil, errConfigChanged
	}
	return validation, nil
}

func (m *manager) attachProcessCgroup(config string, pid int) error {
	if m.cgroupRoot == "" {
		return nil
	}
	path := filepath.Join(m.cgroupRoot, filepath.Base(config))
	created := false
	if err := cgroupMkdir(path, 0o755); err != nil {
		if !os.IsExist(err) {
			return err
		}
	} else {
		created = true
	}
	completed := false
	defer func() {
		if !completed && created {
			_ = cleanupCgroup(path)
		}
	}()
	for _, limit := range []struct {
		name  string
		value string
	}{
		{name: "memory.max", value: m.processMemoryMax},
		{name: "cpu.max", value: m.processCPUQuota},
	} {
		name, value := limit.name, limit.value
		if value == "" {
			continue
		}
		limitPath := filepath.Join(path, name)
		if _, err := cgroupStat(limitPath); err != nil {
			return fmt.Errorf("%s is unavailable: %w", name, err)
		}
		if err := cgroupWriteFile(limitPath, []byte(value+"\n"), 0o600); err != nil {
			return err
		}
	}
	procs := filepath.Join(path, "cgroup.procs")
	if _, err := cgroupStat(procs); err != nil {
		return fmt.Errorf("cgroup.procs is unavailable: %w", err)
	}
	if err := cgroupWriteFile(procs, []byte(strconv.Itoa(pid)+"\n"), 0o600); err != nil {
		return err
	}
	completed = true
	return nil
}

func cleanupCgroup(path string) error {
	if path == "" {
		return nil
	}
	if err := killCgroup(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	deadline := time.Now().Add(100 * time.Millisecond)
	for {
		data, err := os.ReadFile(filepath.Join(path, "cgroup.procs"))
		if os.IsNotExist(err) {
			return os.Remove(path)
		}
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(data)) == "" {
			return os.Remove(path)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("cgroup still contains processes")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func killCgroup(path string) error {
	killPath := filepath.Join(path, "cgroup.kill")
	if _, err := os.Stat(killPath); err != nil {
		return err
	}
	return os.WriteFile(killPath, []byte("1\n"), 0o600)
}

func (m *manager) dryRun(config string) error {
	cmd := exec.Command(m.kmonadCommand, "--dry-run", config)
	cmd.Stdout, cmd.Stderr = childOutputWriters(config)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
	if err := cmd.Start(); err != nil {
		return err
	}
	process := newProcessState(cmd)
	defer closeProcessFD(process)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	if m.dryRunTimeout <= 0 {
		return <-done
	}
	select {
	case err := <-done:
		return err
	case <-time.After(m.dryRunTimeout):
		signalProcess(process, syscall.SIGKILL)
		select {
		case <-done:
		case <-time.After(time.Second):
			return fmt.Errorf("KMonad dry-run timed out after %s and did not exit after forced termination", m.dryRunTimeout)
		}
		return fmt.Errorf("KMonad dry-run timed out after %s", m.dryRunTimeout)
	}
}

func (m *manager) scheduleRetry(config string, state *configState, now time.Time, reason string) {
	state.failures++
	state.failureReason = reason
	delay := retryDelay(state.failures)
	state.retryAfter = now.Add(delay)
	transitionPhase(state, phaseFailed)
	logConfigEvent("retry_scheduled", config, "retry scheduled", map[string]any{"delay_seconds": int(delay / time.Second), "attempt": state.failures})
}

func retryDelay(failures int) time.Duration {
	base := 60 * time.Second
	if failures > 0 && failures < 6 {
		base = time.Duration(1<<failures) * time.Second
	}
	jitterRange := base / 4
	if jitterRange == 0 {
		return base
	}
	return base - jitterRange/2 + retryJitter(jitterRange)
}

func (m *manager) stopProcess(config string, state *configState, deadline time.Time) {
	process := state.process
	if process == nil {
		transitionPhase(state, phaseStopped)
		return
	}
	m.stops.Add(1)
	logConfigEvent("process_stopping", config, "stopping KMonad process", map[string]any{"pid": process.cmd.Process.Pid})
	if !m.ownsProcess(config, process) {
		logConfigEvent("ownership_lost", config, "refusing to signal a process that no longer matches", map[string]any{"pid": process.cmd.Process.Pid})
		state.process = nil
		closeProcessFD(process)
		cleanupLaunchSnapshot(config, process)
		if err := cleanupCgroup(process.cgroupPath); err != nil {
			logConfigEvent("cgroup_cleanup_failed", config, "failed to remove KMonad cgroup", map[string]any{"error": err.Error()})
		}
		transitionPhase(state, phaseStopped)
		return
	}
	signalProcess(process, syscall.SIGTERM)
	if !waitForProcess(process.done, deadline) {
		logConfigEvent("process_killed", config, "KMonad did not stop before the deadline", map[string]any{"pid": process.cmd.Process.Pid})
		signalProcess(process, syscall.SIGKILL)
		_ = waitForProcess(process.done, time.Now().Add(100*time.Millisecond))
	} else {
		state.process = nil
		closeProcessFD(process)
		cleanupLaunchSnapshot(config, process)
		if err := cleanupCgroup(process.cgroupPath); err != nil {
			logConfigEvent("cgroup_cleanup_failed", config, "failed to remove KMonad cgroup", map[string]any{"error": err.Error()})
		}
		return
	}
	state.process = nil
	closeProcessFD(process)
	cleanupLaunchSnapshot(config, process)
	if err := cleanupCgroup(process.cgroupPath); err != nil {
		logConfigEvent("cgroup_cleanup_failed", config, "failed to remove KMonad cgroup", map[string]any{"error": err.Error()})
	}
	transitionPhase(state, phaseStopped)
}

func cleanupLaunchSnapshot(config string, process *processState) {
	if err := removeConfigSnapshot(process.launchPath); err != nil {
		logConfigEvent("snapshot_cleanup_failed", config, "failed to remove validated configuration snapshot", map[string]any{"path": process.launchPath, "error": err.Error()})
	}
}

func waitForProcess(done <-chan struct{}, deadline time.Time) bool {
	remaining := time.Until(deadline)
	if remaining <= 0 {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

func (m *manager) ownsProcess(config string, process *processState) bool {
	if process == nil || process.cmd == nil || process.cmd.Process == nil {
		return false
	}
	pid := process.cmd.Process.Pid
	launchPath := process.launchPath
	if launchPath == "" {
		launchPath = config
	}
	return process.startTick != 0 && processStartTime(pid) == process.startTick && processMatchesCommand(pid, m.kmonadCommand, launchPath)
}

func (m *manager) processHealthy(config string, process *processState) bool {
	if !m.ownsProcess(config, process) {
		return false
	}
	return processStateCode(process.cmd.Process.Pid) != "D"
}

func processStillRunning(process *processState) bool {
	select {
	case <-process.done:
		return false
	default:
		return true
	}
}

func signalProcess(process *processState, signal syscall.Signal) {
	if process == nil || process.cmd == nil || process.cmd.Process == nil {
		return
	}
	if err := signalProcessGroup(process, signal); err == nil {
		return
	}
	pid := process.cmd.Process.Pid
	if process.pidfd != nil {
		if err := signalProcessFD(process.pidfd, signal); err == nil || errors.Is(err, syscall.ESRCH) {
			return
		}
	}
	if process.startTick != 0 && processStartTime(pid) != process.startTick {
		return
	}
	_ = process.cmd.Process.Signal(signal)
}

func signalProcessGroup(process *processState, signal syscall.Signal) error {
	if process == nil || process.cmd == nil || process.cmd.Process == nil {
		return syscall.ESRCH
	}
	groupID := process.processGroupID
	if groupID == 0 {
		groupID = process.cmd.Process.Pid
	}
	if groupID <= 0 {
		return syscall.ESRCH
	}
	return syscall.Kill(-groupID, signal)
}

func (m *manager) cleanup() {
	deadline := time.Now().Add(m.stopTimeout)
	states := m.states
	m.states = make(map[string]*configState)
	m.stopStates(states, deadline)
	if m.statusPath != "" {
		_ = os.Remove(m.statusPath)
	}
}
