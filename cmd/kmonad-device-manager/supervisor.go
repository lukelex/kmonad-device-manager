package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
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

		device, signature, err := readConfig(config)
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
		if changed && state.process != nil {
			if state.pendingSignature == signature && now.Before(state.retryAfter) {
				continue
			}
			if err := m.dryRun(config); err != nil {
				state.pendingSignature = signature
				m.scheduleRetry(config, state, now)
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
		}

		if state.process != nil {
			if !m.processHealthy(config, state.process) {
				if state.process.unhealthySince.IsZero() {
					state.process.unhealthySince = now
				} else if now.Sub(state.process.unhealthySince) >= m.watchdogTimeout {
					logConfigEvent("watchdog_timeout", config, "KMonad process failed the watchdog check", map[string]any{"pid": state.process.cmd.Process.Pid})
					m.stopProcess(config, state, stopDeadline)
					m.scheduleRetry(config, state, now)
					continue
				}
			} else {
				state.process.unhealthySince = time.Time{}
			}
			select {
			case <-state.process.done:
				if state.process.exitErr != nil {
					logConfigEvent("process_exited", config, "KMonad process exited", map[string]any{"error": state.process.exitErr.Error()})
				}
				state.process = nil
				m.scheduleRetry(config, state, now)
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
		m.startConfig(config, state, now)
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
	for config, state := range m.states {
		m.stopProcess(config, state, deadline)
		delete(m.states, config)
	}
}

func (m *manager) startConfig(config string, state *configState, now time.Time) {
	transitionPhase(state, phaseValidating)
	if err := m.dryRun(config); err != nil {
		m.failures.Add(1)
		transitionPhase(state, phaseFailed)
		logConfigEvent("validation_failed", config, "KMonad dry-run validation failed", nil)
		m.scheduleRetry(config, state, now)
		return
	}
	device, err := readDeviceFile(config)
	if err != nil {
		logConfigEvent("configuration_disappeared", config, "configuration disappeared during validation", nil)
		state.retryAfter = now.Add(time.Second)
		transitionPhase(state, phaseWaiting)
		return
	}
	identity, identityErr := deviceID(device)
	if !deviceReady(device) || identityErr != nil || (state.deviceID != "" && identity != state.deviceID) {
		logConfigEvent("device_disappeared", config, "input device disappeared during startup", map[string]any{"device": device})
		state.retryAfter = time.Time{}
		transitionPhase(state, phaseWaiting)
		return
	}

	cmd := exec.Command(m.kmonadCommand, config)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid:   true,
		Pdeathsig: syscall.SIGTERM,
	}
	if err := cmd.Start(); err != nil {
		m.failures.Add(1)
		logConfigEvent("process_start_failed", config, "failed to start KMonad", map[string]any{"error": err.Error()})
		m.scheduleRetry(config, state, now)
		return
	}
	if err := m.attachProcessCgroup(config, cmd.Process.Pid); err != nil {
		logConfigEvent("cgroup_attach_failed", config, "failed to isolate KMonad process", map[string]any{"error": err.Error()})
		signalProcess(cmd, syscall.SIGKILL)
		_ = cmd.Wait()
		m.failures.Add(1)
		m.scheduleRetry(config, state, now)
		return
	}
	process := &processState{cmd: cmd, done: make(chan struct{}), startedAt: now, startTick: processStartTime(cmd.Process.Pid)}
	if m.cgroupRoot != "" {
		process.cgroupPath = filepath.Join(m.cgroupRoot, filepath.Base(config))
	}
	state.process = process
	m.starts.Add(1)
	transitionPhase(state, phaseRunning)
	logConfigEvent("process_started", config, "KMonad process started", map[string]any{"pid": cmd.Process.Pid})
	go func() {
		process.exitErr = cmd.Wait()
		close(process.done)
	}()
}

func (m *manager) attachProcessCgroup(config string, pid int) error {
	if m.cgroupRoot == "" {
		return nil
	}
	path := filepath.Join(m.cgroupRoot, filepath.Base(config))
	if err := os.MkdirAll(path, 0o755); err != nil {
		return err
	}
	for name, value := range map[string]string{
		"memory.max": m.processMemoryMax,
		"cpu.max":    m.processCPUQuota,
	} {
		if value == "" {
			continue
		}
		limitPath := filepath.Join(path, name)
		if _, err := os.Stat(limitPath); err != nil {
			return fmt.Errorf("%s is unavailable: %w", name, err)
		}
		if err := os.WriteFile(limitPath, []byte(value+"\n"), 0o600); err != nil {
			return err
		}
	}
	procs := filepath.Join(path, "cgroup.procs")
	if _, err := os.Stat(procs); err != nil {
		return fmt.Errorf("cgroup.procs is unavailable: %w", err)
	}
	return os.WriteFile(procs, []byte(strconv.Itoa(pid)+"\n"), 0o600)
}

func (m *manager) dryRun(config string) error {
	cmd := exec.Command(m.kmonadCommand, "--dry-run", config)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	if m.dryRunTimeout <= 0 {
		return <-done
	}
	select {
	case err := <-done:
		return err
	case <-time.After(m.dryRunTimeout):
		signalProcess(cmd, syscall.SIGKILL)
		<-done
		return fmt.Errorf("KMonad dry-run timed out after %s", m.dryRunTimeout)
	}
}

func (m *manager) scheduleRetry(config string, state *configState, now time.Time) {
	state.failures++
	delay := 60 * time.Second
	if state.failures < 6 {
		delay = time.Duration(1<<state.failures) * time.Second
	}
	state.retryAfter = now.Add(delay)
	transitionPhase(state, phaseFailed)
	logConfigEvent("retry_scheduled", config, "retry scheduled", map[string]any{"delay_seconds": int(delay / time.Second), "attempt": state.failures})
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
		_ = os.Remove(process.cgroupPath)
		transitionPhase(state, phaseStopped)
		return
	}
	signalProcess(process.cmd, syscall.SIGTERM)
	remaining := time.Until(deadline)
	if remaining < 0 {
		remaining = 0
	}
	select {
	case <-process.done:
		state.process = nil
		_ = os.Remove(process.cgroupPath)
		return
	case <-time.After(remaining):
	}
	if processStillRunning(process) {
		logConfigEvent("process_killed", config, "KMonad did not stop before the deadline", map[string]any{"pid": process.cmd.Process.Pid})
		signalProcess(process.cmd, syscall.SIGKILL)
		select {
		case <-process.done:
		case <-time.After(time.Second):
		}
	}
	state.process = nil
	_ = os.Remove(process.cgroupPath)
	transitionPhase(state, phaseStopped)
}

func (m *manager) ownsProcess(config string, process *processState) bool {
	if process == nil || process.cmd == nil || process.cmd.Process == nil {
		return false
	}
	pid := process.cmd.Process.Pid
	return process.startTick != 0 && processStartTime(pid) == process.startTick &&
		strings.Contains(processCommandLine(pid), filepath.Base(m.kmonadCommand)) &&
		strings.Contains(processCommandLine(pid), filepath.Base(config))
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

func signalProcess(cmd *exec.Cmd, signal syscall.Signal) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if err := syscall.Kill(-cmd.Process.Pid, signal); err != nil {
		_ = cmd.Process.Signal(signal)
	}
}

func (m *manager) cleanup() {
	deadline := time.Now().Add(m.stopTimeout)
	for config, state := range m.states {
		m.stopProcess(config, state, deadline)
	}
	if m.statusPath != "" {
		_ = os.Remove(m.statusPath)
	}
}
