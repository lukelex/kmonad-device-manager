package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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
		maxConfigs:    128,
		states:        make(map[string]*configState),
		duplicates:    make(map[string]string),
	}
	t.Cleanup(m.cleanup)
	return m
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
