package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

func fakeKMonad(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kmonad-test")
	script := `#!/bin/sh
if [ "${1:-}" = --dry-run ]; then
  [ "$(basename "$2")" != invalid.kbd ]
  exit
fi
if [ "$(basename "$1")" = crash.kbd ]; then
  exit 1
fi
trap 'exit 0' TERM INT
while :; do sleep 0.01; done
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeKBD(t *testing.T, path, device string) {
	t.Helper()
	content := "(defcfg\n  input (device-file \"" + device + "\")\n)\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func testManager(t *testing.T, configDir, command string) *manager {
	t.Helper()
	m := &manager{
		configDir:     configDir,
		kmonadCommand: command,
		stopTimeout:   100 * time.Millisecond,
		dryRunTimeout: 100 * time.Millisecond,
		maxConfigs:    128,
		states:        make(map[string]*configState),
		duplicates:    make(map[string]string),
	}
	t.Cleanup(m.cleanup)
	return m
}

func scriptCommand(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kmonad-test")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition did not become true")
}

func TestEnvironmentValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "env")
	if err := os.WriteFile(path, []byte("# comment\r\n KMONAD_CONFIG_DIR = \"/tmp/kmonad\"\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := environmentValue(path, "KMONAD_CONFIG_DIR")
	if err != nil {
		t.Fatal(err)
	}
	if value != "/tmp/kmonad" {
		t.Fatalf("expected /tmp/kmonad, got %q", value)
	}
}

func TestLoadSettingsReadsEnvironment(t *testing.T) {
	t.Setenv("KMONAD_CONFIG_DIR", "/tmp/kmonad")
	t.Setenv("KMONAD_COMMAND", "kmonad-test")
	t.Setenv("KMONAD_POLL_INTERVAL", "3")
	t.Setenv("KMONAD_STOP_TIMEOUT", "4")
	t.Setenv("KMONAD_DRY_RUN_TIMEOUT", "5")
	t.Setenv("KMONAD_WATCHDOG_TIMEOUT", "6")
	t.Setenv("KMONAD_MAX_CONFIGS", "7")
	t.Setenv("KMONAD_METRICS_ADDR", "127.0.0.1:9090")
	t.Setenv("KMONAD_CGROUP_ROOT", "/tmp/cgroup")
	t.Setenv("KMONAD_PROCESS_MEMORY_MAX", "64M")
	t.Setenv("KMONAD_PROCESS_CPU_MAX", "50%")
	s := loadSettings()
	if s.configDir != "/tmp/kmonad" || s.kmonadCommand != "kmonad-test" || s.pollInterval != 3*time.Second || s.stopTimeout != 4*time.Second || s.dryRunTimeout != 5*time.Second || s.watchdogTimeout != 6*time.Second || s.maxConfigs != 7 || s.metricsAddr != "127.0.0.1:9090" || s.cgroupRoot != "/tmp/cgroup" || s.processMemoryMax != "64M" || s.processCPUQuota != "50%" {
		t.Fatalf("unexpected settings: %#v", s)
	}
}

func TestValidateSettingsRejectsInvalidValues(t *testing.T) {
	valid := settings{pollInterval: time.Second, stopTimeout: time.Second, dryRunTimeout: time.Second, watchdogTimeout: time.Second, maxConfigs: 1}
	if err := validateSettings(valid); err != nil {
		t.Fatalf("valid settings rejected: %v", err)
	}
	for _, field := range []string{"poll", "stop", "dry-run", "watchdog", "configs"} {
		s := valid
		switch field {
		case "poll":
			s.pollInterval = 0
		case "stop":
			s.stopTimeout = 0
		case "dry-run":
			s.dryRunTimeout = 0
		case "watchdog":
			s.watchdogTimeout = 0
		case "configs":
			s.maxConfigs = 0
		}
		if err := validateSettings(s); err == nil {
			t.Fatalf("%s setting was accepted", field)
		}
	}
}

func TestValidateSettingsRestrictsMetricsToLoopback(t *testing.T) {
	valid := settings{pollInterval: time.Second, stopTimeout: time.Second, dryRunTimeout: time.Second, watchdogTimeout: time.Second, maxConfigs: 1}
	for _, address := range []string{"127.0.0.1:9090", "[::1]:9090", "localhost:9090"} {
		valid.metricsAddr = address
		if err := validateSettings(valid); err != nil {
			t.Fatalf("loopback metrics address %q rejected: %v", address, err)
		}
	}
	valid.metricsAddr = ":9090"
	if err := validateSettings(valid); err == nil {
		t.Fatal("wildcard metrics address was accepted without opt-in")
	}
	valid.metricsAllowRemote = true
	if err := validateSettings(valid); err != nil {
		t.Fatalf("explicit remote metrics opt-in rejected: %v", err)
	}
}

func TestRuntimeDirUsesEnvironment(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "/tmp/runtime")
	path, err := runtimeDir()
	if err != nil || path != "/tmp/runtime" {
		t.Fatalf("unexpected runtime directory: %q, %v", path, err)
	}
}

func TestShowStatusJSONVerifiesManagerIdentity(t *testing.T) {
	runtime := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	pid := os.Getpid()
	status := statusFile{PID: pid, ProcessStart: processStartTime(pid), UpdatedAt: time.Now(), ConfigDir: "/tmp/kmonad"}
	data, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runtime, "status.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	output := captureStdout(t, func() {
		if code := showStatus(true); code != 0 {
			t.Fatalf("unexpected status exit code: %d", code)
		}
	})
	var actual statusFile
	if err := json.Unmarshal([]byte(output), &actual); err != nil || actual.PID != pid || actual.ProcessStart != status.ProcessStart {
		t.Fatalf("unexpected JSON status: %q, %v", output, err)
	}
	status.ProcessStart++
	data, err = json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runtime, "status.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if code := showStatus(false); code != 3 {
		t.Fatalf("stale manager identity returned %d", code)
	}
}

func captureStdout(t *testing.T, run func()) string {
	t.Helper()
	previous := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	t.Cleanup(func() { os.Stdout = previous })
	run()
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestDryRunTimesOut(t *testing.T) {
	command := scriptCommand(t, `if [ "${1:-}" = --dry-run ]; then sleep 10; fi`)
	m := testManager(t, t.TempDir(), command)
	m.dryRunTimeout = 20 * time.Millisecond
	started := time.Now()
	err := m.dryRun("config.kbd")
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected dry-run timeout, got %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("dry-run timeout took too long")
	}
}

func TestStopProcessKillsTERMResistantProcessGroup(t *testing.T) {
	command := scriptCommand(t, `trap '' TERM INT; while :; do sleep 1; done`)
	m := testManager(t, t.TempDir(), command)
	config := filepath.Join(m.configDir, "stubborn.kbd")
	cmd := exec.Command(command, config)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	process := &processState{cmd: cmd, done: make(chan struct{}), startTick: processStartTime(cmd.Process.Pid)}
	state := &configState{phase: phaseRunning, process: process}
	waitFor(t, func() bool { return processCommandLine(cmd.Process.Pid) != "" })
	if !m.ownsProcess(config, process) {
		t.Fatalf("expected test process to be owned: start=%d current=%d cmdline=%q", process.startTick, processStartTime(cmd.Process.Pid), processCommandLine(cmd.Process.Pid))
	}
	go func() { _ = cmd.Wait(); close(process.done) }()
	m.stopProcess(config, state, time.Now().Add(20*time.Millisecond))
	if pidExists(cmd.Process.Pid) {
		t.Fatal("TERM-resistant process group was not killed")
	}
}

func TestConfigurationStateMachineReachesRunning(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	if err := os.Mkdir(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(configDir, "keyboard.kbd")
	writeKBD(t, config, "/dev/null")
	m := testManager(t, configDir, fakeKMonad(t))
	m.reconcile(time.Now())
	if state := m.states[config]; state == nil || state.phase != phaseRunning {
		t.Fatalf("expected running phase, got %#v", state)
	}
}

func TestStateMachineRejectsInvalidTransition(t *testing.T) {
	state := &configState{phase: phaseRunning}
	if transitionPhase(state, phaseValidating) {
		t.Fatal("running configuration must not transition directly to validating")
	}
	if state.phase != phaseRunning {
		t.Fatalf("invalid transition changed phase to %q", state.phase)
	}
}

func TestReconcileStopsProcessAfterPermissionChange(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	if err := os.Mkdir(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(configDir, "keyboard.kbd")
	writeKBD(t, config, "/dev/null")
	m := testManager(t, configDir, fakeKMonad(t))
	state := &configState{phase: phaseRunning}
	m.states[config] = state
	if err := os.Chmod(config, 0o666); err != nil {
		t.Fatal(err)
	}
	m.reconcile(time.Now())
	if _, ok := m.states[config]; ok {
		t.Fatal("permission change should stop and remove the configuration state")
	}
}

func TestRunFallsBackWhenWatcherInitializationFails(t *testing.T) {
	previous := newWatcher
	newWatcher = func() (*fsnotify.Watcher, error) { return nil, errors.New("injected watcher failure") }
	defer func() { newWatcher = previous }()
	m := testManager(t, t.TempDir(), fakeKMonad(t))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	m.run(ctx, time.Millisecond)
}

func TestMetricsExposeCounters(t *testing.T) {
	m := testManager(t, t.TempDir(), fakeKMonad(t))
	m.reconciles.Store(3)
	m.starts.Store(2)
	m.failures.Store(1)
	record := httptest.NewRecorder()
	metricsHandler(m)(record, httptest.NewRequest("GET", "/metrics", nil))
	body := record.Body.String()
	for _, metric := range []string{
		"kmonad_manager_reconciles_total 3",
		"kmonad_manager_process_starts_total 2",
		"kmonad_manager_failures_total 1",
	} {
		if !strings.Contains(body, metric) {
			t.Fatalf("missing metric %q in %s", metric, body)
		}
	}
}

func TestMetricsServerUsesBoundedHTTPSettings(t *testing.T) {
	m := testManager(t, t.TempDir(), fakeKMonad(t))
	server, err := startMetricsServer(m, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	if server.ReadHeaderTimeout != 5*time.Second || server.IdleTimeout != 30*time.Second || server.MaxHeaderBytes != 8<<10 {
		t.Fatalf("unexpected metrics server limits: %#v", server)
	}
}

func TestRestoreBackoffFromStatus(t *testing.T) {
	m := testManager(t, "/tmp/kmonad-config", fakeKMonad(t))
	when := time.Now().Add(time.Minute).Truncate(time.Second)
	m.restoreBackoff(&statusFile{Configurations: []statusConfig{{
		Name:       "keyboard.kbd",
		Failures:   3,
		RetryAfter: when,
	}}})
	state := m.states[filepath.Join(m.configDir, "keyboard.kbd")]
	if state == nil || state.failures != 3 || !state.retryAfter.Equal(when) || state.phase != phaseFailed {
		t.Fatalf("backoff was not restored: %#v", state)
	}
}

func TestStatusIncludesConnectionAndHealthDetails(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	if err := os.Mkdir(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(configDir, "keyboard.kbd")
	writeKBD(t, config, "/dev/null")
	m := testManager(t, configDir, fakeKMonad(t))
	m.statusPath = filepath.Join(root, "status.json")
	m.reconcile(time.Now())
	status := readStatusFile(m.statusPath)
	if status == nil || len(status.Configurations) != 1 {
		t.Fatalf("missing status details: %#v", status)
	}
	item := status.Configurations[0]
	if !item.Connected || !item.Healthy || item.State != "running" || item.Reason != "process healthy" {
		t.Fatalf("unexpected status details: %#v", item)
	}
}

func TestProcessOwnershipRejectsChangedIdentity(t *testing.T) {
	command := fakeKMonad(t)
	m := testManager(t, t.TempDir(), command)
	cmd := exec.Command(command, filepath.Join(m.configDir, "keyboard.kbd"))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	process := &processState{cmd: cmd, startTick: processStartTime(cmd.Process.Pid) + 1}
	if m.ownsProcess("keyboard.kbd", process) {
		t.Fatal("changed process identity should not be owned")
	}
	signalProcessID(cmd.Process.Pid, syscall.SIGKILL)
	_ = cmd.Wait()
}

func TestConfigDeletionDuringValidationDoesNotStartProcess(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "keyboard.kbd")
	writeKBD(t, config, "/dev/null")
	command := scriptCommand(t, `if [ "${1:-}" = --dry-run ]; then rm -f "$2"; fi`)
	m := testManager(t, root, command)
	state := &configState{phase: phaseDiscovered, deviceID: deviceIdentityForTest(t, "/dev/null")}
	_, signature, err := readConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	m.startConfig(config, state, time.Now(), signature)
	if state.process != nil || state.phase != phaseWaiting {
		t.Fatalf("deleted configuration was started: %#v", state)
	}
}

func TestDeviceRemovalDuringStartupDoesNotStartProcess(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "keyboard.kbd")
	writeKBD(t, config, "/dev/null")
	command := scriptCommand(t, `if [ "${1:-}" = --dry-run ]; then sed -i 's#/dev/null#/dev/does-not-exist#' "$2"; fi`)
	m := testManager(t, root, command)
	state := &configState{phase: phaseDiscovered, deviceID: deviceIdentityForTest(t, "/dev/null")}
	_, signature, err := readConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	m.startConfig(config, state, time.Now(), signature)
	if state.process != nil || state.phase != phaseWaiting {
		t.Fatalf("removed device configuration was started: %#v", state)
	}
}

func TestConfigurationChangeDuringValidationDoesNotStartChangedFile(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "keyboard.kbd")
	writeKBD(t, config, "/dev/null")
	command := scriptCommand(t, `if [ "${1:-}" = --dry-run ]; then printf '(invalid\n' > "$2"; fi`)
	m := testManager(t, root, command)
	state := &configState{phase: phaseDiscovered, deviceID: deviceIdentityForTest(t, "/dev/null")}
	_, signature, err := readConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	m.startConfig(config, state, time.Now(), signature)
	if state.process != nil || state.phase != phaseWaiting {
		t.Fatalf("changed configuration was started: %#v", state)
	}
}

func deviceIdentityForTest(t *testing.T, path string) string {
	t.Helper()
	identity, err := deviceID(path)
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func TestCorruptStatusIsIgnoredAndRemovedDuringRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status.json")
	if err := os.WriteFile(path, []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if readStatusFile(path) != nil {
		t.Fatal("corrupt status should not decode")
	}
	recoverOwnedProcesses(path, fakeKMonad(t))
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("corrupt status was not removed: %v", err)
	}
}

func TestAcquireLockReportsRuntimeDirectoryFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime-file")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", path)
	if _, _, err := acquireLock(); err == nil {
		t.Fatal("expected runtime directory creation failure")
	}
}

func TestWatcherLossIsRecoveredByRecreatingWatcher(t *testing.T) {
	previous := newWatcher
	var calls atomic.Int32
	newWatcher = func() (*fsnotify.Watcher, error) {
		if calls.Add(1) == 1 {
			watcher, err := fsnotify.NewWatcher()
			if err == nil {
				_ = watcher.Close()
			}
			return watcher, err
		}
		return nil, errors.New("watcher unavailable")
	}
	defer func() { newWatcher = previous }()
	m := testManager(t, t.TempDir(), fakeKMonad(t))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	m.run(ctx, time.Millisecond)
	if calls.Load() < 2 {
		t.Fatalf("watcher was not recreated after loss: %d attempts", calls.Load())
	}
}

func TestSystemdNotifySendsDatagram(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "notify.sock")
	listener, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: socketPath, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	t.Setenv("NOTIFY_SOCKET", socketPath)
	systemdNotify("READY=1")
	if err := listener.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 128)
	n, _, err := listener.ReadFromUnix(buffer)
	if err != nil {
		t.Fatal(err)
	}
	if string(buffer[:n]) != "READY=1\n" {
		t.Fatalf("unexpected notification: %q", buffer[:n])
	}
}

func TestSystemdWatchdogSendsHeartbeat(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "notify.sock")
	listener, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: socketPath, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	t.Setenv("NOTIFY_SOCKET", socketPath)
	t.Setenv("WATCHDOG_USEC", "20000")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go systemdWatchdog(ctx)
	if err := listener.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 128)
	n, _, err := listener.ReadFromUnix(buffer)
	if err != nil {
		t.Fatal(err)
	}
	if string(buffer[:n]) != "WATCHDOG=1\n" {
		t.Fatalf("unexpected watchdog notification: %q", buffer[:n])
	}
}

func TestLastKnownGoodProcessSurvivesInvalidConfigurationUpdate(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "keyboard.kbd")
	writeKBD(t, config, "/dev/null")
	command := scriptCommand(t, `if [ "${1:-}" = --dry-run ] && grep -q invalid "$2"; then exit 1; fi; if [ "${1:-}" != --dry-run ]; then trap 'exit 0' TERM INT; while :; do sleep 0.01; done; fi`)
	m := testManager(t, root, command)
	m.reconcile(time.Now())
	state := m.states[config]
	if state == nil || state.process == nil {
		t.Fatal("initial known-good configuration did not start")
	}
	pid := state.process.cmd.Process.Pid
	if err := os.WriteFile(config, []byte("invalid (defcfg input (device-file \"/dev/null\"))\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m.reconcile(time.Now())
	if state.process == nil || state.process.cmd.Process.Pid != pid {
		t.Fatal("invalid update displaced the last-known-good process")
	}
	if state.pendingSignature == "" {
		t.Fatal("invalid update was not recorded as pending")
	}
}

func TestAttachProcessCgroupWritesLimitsAndPID(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "keyboard.kbd")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"memory.max", "cpu.max", "cgroup.procs"} {
		if err := os.WriteFile(filepath.Join(child, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	m := testManager(t, t.TempDir(), fakeKMonad(t))
	m.cgroupRoot = root
	m.processMemoryMax = "64M"
	m.processCPUQuota = "50000 100000"
	if err := m.attachProcessCgroup(filepath.Join(m.configDir, "keyboard.kbd"), 1234); err != nil {
		t.Fatal(err)
	}
	for name, expected := range map[string]string{"memory.max": "64M\n", "cpu.max": "50000 100000\n", "cgroup.procs": "1234\n"} {
		data, err := os.ReadFile(filepath.Join(child, name))
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != expected {
			t.Fatalf("%s: expected %q, got %q", name, expected, data)
		}
	}
}

func TestAttachProcessCgroupIntegration(t *testing.T) {
	root := os.Getenv("KMONAD_TEST_CGROUP_ROOT")
	if root == "" {
		t.Skip("set KMONAD_TEST_CGROUP_ROOT to a delegated cgroup v2 directory")
	}
	command := fakeKMonad(t)
	cmd := exec.Command(command, "keyboard.kbd")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		signalProcessID(cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	})
	m := testManager(t, t.TempDir(), command)
	m.cgroupRoot = root
	if err := m.attachProcessCgroup("keyboard.kbd", cmd.Process.Pid); err != nil {
		t.Fatalf("cannot attach process to delegated cgroup: %v", err)
	}
}

func TestReadDeviceFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keyboard.kbd")
	content := "(defcfg\n  input(device-file \"/dev/input/event0\")\n)\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := readDeviceFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if value != "/dev/input/event0" {
		t.Fatalf("expected device path, got %q", value)
	}
}

func TestDeviceFileParserIgnoresCommentsAndStringContents(t *testing.T) {
	content := []byte(`(defcfg
  ;; input (device-file "/dev/wrong")
  output (uinput-sink "input (device-file \\\"/dev/wrong\\\")")
  input(device-file "/dev/input/event0")
)`)
	device, err := deviceFileFromData(content)
	if err != nil {
		t.Fatal(err)
	}
	if device != "/dev/input/event0" {
		t.Fatalf("expected /dev/input/event0, got %q", device)
	}
}

func TestDeviceFileParserRejectsUnterminatedStrings(t *testing.T) {
	if _, err := deviceFileFromData([]byte(`(defcfg input (device-file "/dev/input/event0)`)); err == nil {
		t.Fatal("expected unterminated string to fail")
	}
}

func TestSecondsRejectsOverflow(t *testing.T) {
	if got := seconds("0"); got != 0 {
		t.Fatalf("expected zero duration for zero input, got %s", got)
	}
	if got := seconds("999999999999999999999999"); got != 0 {
		t.Fatalf("expected zero duration for overflow, got %s", got)
	}
	if got := seconds("3"); got != 3*time.Second {
		t.Fatalf("unexpected valid duration: %s", got)
	}
}

func TestReconcileStartsReadyPrimaryAndSkipsDuplicate(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	if err := os.Mkdir(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	primary := filepath.Join(configDir, "one.kbd")
	duplicate := filepath.Join(configDir, "two.kbd")
	missing := filepath.Join(configDir, "missing.kbd")
	writeKBD(t, primary, "/dev/null")
	writeKBD(t, duplicate, "/dev/null")
	writeKBD(t, missing, filepath.Join(root, "not-connected"))

	m := testManager(t, configDir, fakeKMonad(t))
	m.reconcile(time.Now())

	if m.states[primary] == nil || m.states[primary].process == nil {
		t.Fatal("expected primary configuration to be running")
	}
	if _, ok := m.states[duplicate]; ok {
		t.Fatal("duplicate configuration should not be started")
	}
	if _, ok := m.states[missing]; ok {
		t.Fatal("disconnected configuration should not be tracked")
	}
	if m.duplicates[duplicate] == "" {
		t.Fatal("expected duplicate configuration to be recorded")
	}
}

func TestReconcileRefusesWorldWritableConfiguration(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	if err := os.Mkdir(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(configDir, "unsafe.kbd")
	writeKBD(t, config, "/dev/null")
	if err := os.Chmod(config, 0o666); err != nil {
		t.Fatal(err)
	}
	m := testManager(t, configDir, fakeKMonad(t))
	m.reconcile(time.Now())
	if _, ok := m.states[config]; ok {
		t.Fatal("world-writable configuration should not be started")
	}
}

func TestReconcileRefusesWorldWritableConfigurationDirectory(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	if err := os.Mkdir(configDir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(configDir, 0o777); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(configDir, "unsafe.kbd")
	writeKBD(t, config, "/dev/null")
	m := testManager(t, configDir, fakeKMonad(t))
	m.reconcile(time.Now())
	if _, ok := m.states[config]; ok {
		t.Fatal("world-writable configuration directory should not be started")
	}
}

func TestReconcileHonorsConfigurationLimit(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	if err := os.Mkdir(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(configDir, "one.kbd")
	second := filepath.Join(configDir, "two.kbd")
	writeKBD(t, first, "/dev/null")
	writeKBD(t, second, "/dev/zero")
	m := testManager(t, configDir, fakeKMonad(t))
	m.maxConfigs = 1
	m.reconcile(time.Now())
	if m.states[first] == nil || m.states[first].process == nil {
		t.Fatal("expected the first configuration to run")
	}
	if _, ok := m.states[second]; ok {
		t.Fatal("configuration beyond the limit should not run")
	}
}

func TestReconcileDuplicateFailsOverWhenPrimaryIsRemoved(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	if err := os.Mkdir(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	primary := filepath.Join(configDir, "one.kbd")
	duplicate := filepath.Join(configDir, "two.kbd")
	writeKBD(t, primary, "/dev/null")
	writeKBD(t, duplicate, "/dev/null")
	m := testManager(t, configDir, fakeKMonad(t))
	m.reconcile(time.Now())
	if err := os.Remove(primary); err != nil {
		t.Fatal(err)
	}
	m.reconcile(time.Now())
	if m.states[duplicate] == nil || m.states[duplicate].process == nil {
		t.Fatal("expected duplicate configuration to take over")
	}
	if _, ok := m.states[primary]; ok {
		t.Fatal("removed primary configuration should not remain tracked")
	}
}

func TestReconcileRestartsWhenDeviceTargetChanges(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	device := filepath.Join(root, "device")
	if err := os.Mkdir(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/dev/null", device); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(configDir, "one.kbd")
	writeKBD(t, config, device)
	m := testManager(t, configDir, fakeKMonad(t))
	m.reconcile(time.Now())
	oldPID := m.states[config].process.cmd.Process.Pid
	if err := os.Remove(device); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/dev/zero", device); err != nil {
		t.Fatal(err)
	}
	m.reconcile(time.Now())
	newPID := m.states[config].process.cmd.Process.Pid
	if newPID == oldPID {
		t.Fatalf("expected a new process after device replacement, still have PID %d", oldPID)
	}
}

func TestReconcileBacksOffARepeatedCrash(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	if err := os.Mkdir(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(configDir, "crash.kbd")
	writeKBD(t, config, "/dev/null")
	m := testManager(t, configDir, fakeKMonad(t))
	m.reconcile(time.Now())
	process := m.states[config].process
	waitFor(t, func() bool { return !processStillRunning(process) })
	now := time.Now()
	m.reconcile(now)
	state := m.states[config]
	if state.failures != 1 {
		t.Fatalf("expected one recorded failure, got %d", state.failures)
	}
	if !state.retryAfter.After(now) {
		t.Fatal("expected a future retry deadline")
	}
	if state.process != nil {
		t.Fatal("crashed process should not be immediately restarted")
	}
}

func TestLockPreventsConcurrentManagers(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	first, _, err := acquireLock()
	if err != nil {
		t.Fatal(err)
	}
	defer releaseLock(first)
	second, _, err := acquireLock()
	if !errors.Is(err, errLockHeld) {
		t.Fatalf("expected lock contention, got file=%v err=%v", second, err)
	}
}

func TestJSONLogIncludesStructuredFields(t *testing.T) {
	var output bytes.Buffer
	previousOutput := logOutput
	logOutput = &output
	defer func() { logOutput = previousOutput }()
	t.Setenv("KMONAD_LOG_FORMAT", "json")

	logConfigEvent("process_started", "/tmp/keyboard.kbd", "KMonad process started", map[string]any{"pid": 42})
	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record["event"] != "process_started" || record["config"] != "keyboard.kbd" || record["pid"] != float64(42) {
		t.Fatalf("unexpected structured log: %#v", record)
	}
}

func TestRefreshWatchesConfigurationAndDeviceDirectories(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	deviceDir := filepath.Join(root, "devices")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(deviceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	device := filepath.Join(deviceDir, "keyboard")
	if err := os.Symlink("/dev/null", device); err != nil {
		t.Fatal(err)
	}
	writeKBD(t, filepath.Join(configDir, "keyboard.kbd"), device)
	m := testManager(t, configDir, fakeKMonad(t))
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer watcher.Close()

	m.refreshWatches(watcher)
	if !m.watchPaths[configDir] || !m.watchPaths[deviceDir] {
		t.Fatalf("expected config and device directories to be watched: %#v", m.watchPaths)
	}
}

func TestRecoverOwnedProcessFromStaleStatus(t *testing.T) {
	command := fakeKMonad(t)
	config := filepath.Join(t.TempDir(), "recover.kbd")
	cmd := exec.Command(command, config)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		signalProcessID(cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	})
	start := processStartTime(cmd.Process.Pid)
	if start == 0 {
		t.Fatal("could not read owned process start time")
	}
	statusPath := filepath.Join(t.TempDir(), "status.json")
	data, err := json.Marshal(statusFile{Configurations: []statusConfig{{
		Name:         filepath.Base(config),
		ProcessID:    cmd.Process.Pid,
		ProcessStart: start,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statusPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	recoverOwnedProcesses(statusPath, command)
	if pidExists(cmd.Process.Pid) {
		t.Fatal("stale owned process was not terminated")
	}
	if _, err := os.Stat(statusPath); !os.IsNotExist(err) {
		t.Fatalf("expected stale status file to be removed, got %v", err)
	}
	_ = cmd.Wait()
}

func TestRecoverOwnedProcessRejectsReusedPID(t *testing.T) {
	command := fakeKMonad(t)
	config := filepath.Join(t.TempDir(), "recover.kbd")
	cmd := exec.Command(command, config)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		signalProcessID(cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	}()
	statusPath := filepath.Join(t.TempDir(), "status.json")
	data, err := json.Marshal(statusFile{Configurations: []statusConfig{{
		Name:         filepath.Base(config),
		ProcessID:    cmd.Process.Pid,
		ProcessStart: processStartTime(cmd.Process.Pid) + 1,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statusPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	recoverOwnedProcesses(statusPath, command)
	if !pidExists(cmd.Process.Pid) {
		t.Fatal("PID start-time mismatch should not terminate the process")
	}
}

func FuzzReadDeviceFile(f *testing.F) {
	f.Add("(defcfg input (device-file \"/dev/null\"))")
	f.Add("; input (device-file \"/dev/null\")")
	f.Fuzz(func(t *testing.T, content string) {
		path := filepath.Join(t.TempDir(), "config.kbd")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		_, _, _ = readConfig(path)
	})
}
