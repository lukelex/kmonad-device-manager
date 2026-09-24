//go:build linux

package platform

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	"encoding/binary"

	"golang.org/x/sys/unix"
)

type fixtureInputBackend struct {
	keyboards []KeyboardDevice
	observer  KeypressObserver
}

func (backend fixtureInputBackend) ListKeyboards() ([]KeyboardDevice, error) {
	return backend.keyboards, nil
}

func (backend fixtureInputBackend) KeypressObserver(string) (KeypressObserver, error) {
	return backend.observer, nil
}

type fixtureKeypressObserver struct{}

func (fixtureKeypressObserver) WaitForKeypress(context.Context) error { return nil }

func TestConfigureCgroupCleansEachFailedFileOperation(t *testing.T) {
	previousStat := cgroupStat
	previousWrite := cgroupWriteFile
	defer func() {
		cgroupStat = previousStat
		cgroupWriteFile = previousWrite
	}()
	cgroupStat = func(string) (os.FileInfo, error) { return nil, nil }

	for _, failedFile := range []string{"memory.max", "cpu.max", "cgroup.procs"} {
		t.Run(failedFile, func(t *testing.T) {
			root := t.TempDir()
			cgroupWriteFile = func(path string, data []byte, perm os.FileMode) error {
				if filepath.Base(path) == failedFile {
					return errors.New("injected cgroup write failure")
				}
				return nil
			}
			if _, err := (defaultSystem{}).ConfigureCgroup(root, "keyboard.kbd", 1234, "64M", "50000 100000"); err == nil {
				t.Fatal("injected cgroup failure unexpectedly succeeded")
			}
			if _, err := os.Stat(filepath.Join(root, "keyboard.kbd")); !os.IsNotExist(err) {
				t.Fatalf("failed cgroup setup leaked its directory: %v", err)
			}
		})
	}
}

func TestListenAPISocketSecuresSocketAndRemovesItOnClose(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "api.sock")
	listener, err := (defaultSystem{}).ListenAPISocket(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0o600 {
		t.Fatalf("unexpected API socket mode: %v", info.Mode())
	}
	accepted := make(chan APIConnection, 1)
	acceptErr := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			acceptErr <- err
			return
		}
		accepted <- connection
	}()
	connection, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	select {
	case peer := <-accepted:
		_ = peer.Close()
	case err := <-acceptErr:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("same-user API connection was not accepted")
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("API socket was not removed: %v", err)
	}
}

func TestListenAPISocketRefusesUnsafeExistingPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api.sock")
	if err := os.WriteFile(path, []byte("do not replace"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (defaultSystem{}).ListenAPISocket(path); err == nil {
		t.Fatal("non-socket API path was replaced")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "do not replace" {
		t.Fatalf("unsafe API path was changed: %q, %v", data, err)
	}
}

func TestAPISocketPathUsesPrivateServiceDirectory(t *testing.T) {
	runtime := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	if _, err := (defaultSystem{}).APISocketPath(); err == nil {
		t.Fatal("group-accessible runtime directory was accepted")
	}
	if err := os.Chmod(runtime, 0o700); err != nil {
		t.Fatal(err)
	}
	path, err := (defaultSystem{}).APISocketPath()
	if err != nil {
		t.Fatal(err)
	}
	wanted := filepath.Join(runtime, "kmonad-device-manager", "api.sock")
	if path != wanted {
		t.Fatalf("unexpected API socket path: %q", path)
	}
	info, err := os.Lstat(filepath.Dir(path))
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("API directory was not secured: %v, %v", info, err)
	}
}

func TestDeviceAvailabilityDistinguishesUnavailableStates(t *testing.T) {
	if got := (defaultSystem{}).DeviceAvailability("/dev/null"); got != DeviceConnected {
		t.Fatalf("character-device availability = %q, want %q", got, DeviceConnected)
	}
	if got := (defaultSystem{}).DeviceAvailability(filepath.Join(t.TempDir(), "missing")); got != DeviceDisconnected {
		t.Fatalf("missing device availability = %q, want %q", got, DeviceDisconnected)
	}
	regularFile := filepath.Join(t.TempDir(), "regular")
	if err := os.WriteFile(regularFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := (defaultSystem{}).DeviceAvailability(regularFile); got != DeviceUnsupported {
		t.Fatalf("regular-file availability = %q, want %q", got, DeviceUnsupported)
	}

	previousStat := deviceStat
	deviceStat = func(string) (os.FileInfo, error) { return nil, errors.New("permission denied") }
	t.Cleanup(func() { deviceStat = previousStat })
	if got := (defaultSystem{}).DeviceAvailability("/dev/input/event0"); got != DeviceInaccessible {
		t.Fatalf("failed-stat availability = %q, want %q", got, DeviceInaccessible)
	}
}

func TestRenderKMonadInputQuotesThePrivateDevicePath(t *testing.T) {
	rendered, err := (defaultSystem{}).RenderKMonadInput("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	if rendered != `input (device-file "/dev/null")` {
		t.Fatalf("unexpected rendered input: %q", rendered)
	}
}

func TestRenderKMonadDefcfgOwnsInputAndUinputOutput(t *testing.T) {
	rendered, err := (defaultSystem{}).RenderKMonadDefcfg("/dev/null", "kmonad-device-manager-test")
	if err != nil {
		t.Fatal(err)
	}
	want := "(defcfg\n  input (device-file \"/dev/null\")\n  output (uinput-sink \"kmonad-device-manager-test\")\n)"
	if rendered != want {
		t.Fatalf("unexpected rendered defcfg: %q", rendered)
	}
	if _, err := (defaultSystem{}).RenderKMonadDefcfg("/dev/null", ""); err == nil {
		t.Fatal("empty output name was accepted")
	}
}

func TestLinuxInputBackendIsInjectable(t *testing.T) {
	previous := inputBackend
	inputBackend = fixtureInputBackend{
		keyboards: []KeyboardDevice{{Identity: "fixture", DisplayName: "Fixture keyboard", Availability: DeviceConnected}},
		observer:  fixtureKeypressObserver{},
	}
	t.Cleanup(func() { inputBackend = previous })

	devices, err := (defaultSystem{}).ListKeyboards()
	if err != nil || len(devices) != 1 || devices[0].DisplayName != "Fixture keyboard" {
		t.Fatalf("fixture discovery result = %#v, %v", devices, err)
	}
	observer, err := (defaultSystem{}).KeypressObserver("fixture")
	if err != nil {
		t.Fatal(err)
	}
	if err := observer.WaitForKeypress(context.Background()); err != nil {
		t.Fatalf("fixture observer failed: %v", err)
	}
}

func TestRealKeyboardDiscovery(t *testing.T) {
	if os.Getenv("KMONAD_TEST_REAL_INPUT") != "1" {
		t.Skip("set KMONAD_TEST_REAL_INPUT=1 to test host keyboard discovery")
	}
	previous := inputBackend
	inputBackend = sysfsInputBackend{}
	t.Cleanup(func() { inputBackend = previous })
	devices, err := (defaultSystem{}).ListKeyboards()
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) == 0 {
		t.Fatal("no keyboard-capable input interfaces discovered")
	}
	for _, device := range devices {
		if device.Identity == "" || device.NodePath == "" {
			t.Fatalf("incomplete real-device discovery result: %#v", device)
		}
	}
}

func TestContainsKeypressIgnoresKeyRepeats(t *testing.T) {
	timevalSize := int(unsafe.Sizeof(unix.Timeval{}))
	eventSize := timevalSize + 8
	data := make([]byte, eventSize*2)
	binary.NativeEndian.PutUint16(data[timevalSize:], unix.EV_KEY)
	binary.NativeEndian.PutUint32(data[timevalSize+4:], 2)
	binary.NativeEndian.PutUint16(data[eventSize+timevalSize:], unix.EV_KEY)
	binary.NativeEndian.PutUint32(data[eventSize+timevalSize+4:], 1)
	if !containsKeypress(data, timevalSize) {
		t.Fatal("keypress was not found")
	}
	binary.NativeEndian.PutUint32(data[eventSize+timevalSize+4:], 0)
	if containsKeypress(data, timevalSize) {
		t.Fatal("key release or repeat was treated as a keypress")
	}
}

func TestListKeyboardsFiltersPointerOnlyDevices(t *testing.T) {
	previousRoot := inputSysfsRoot
	inputSysfsRoot = t.TempDir()
	defer func() { inputSysfsRoot = previousRoot }()
	writeInputDevice := func(t *testing.T, event, capabilities, name string) {
		t.Helper()
		root := filepath.Join(inputSysfsRoot, event, "device")
		if err := os.MkdirAll(filepath.Join(root, "capabilities"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "capabilities", "key"), []byte(capabilities), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "name"), []byte(name+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(root, "id"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "id", "vendor"), []byte("046d\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeInputDevice(t, "event0", "1000 40000000", "Keyboard")
	writeInputDevice(t, "event1", "40000000", "Pointer")
	devices, err := (defaultSystem{}).ListKeyboards()
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 || devices[0].DisplayName != "Keyboard" || devices[0].Vendor != "046d" || devices[0].IdentityStability != "topology" {
		t.Fatalf("unexpected keyboard discovery result: %#v", devices)
	}
}

func TestProcessMatchesEnvShebang(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kmonad-test")
	if err := os.WriteFile(path, []byte("#!/usr/bin/env bash\nwhile true; do sleep 1; done\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(t.TempDir(), "snapshot.kbd")
	command := exec.Command(path, config)
	(defaultSystem{}).ConfigureChild(command)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = (defaultSystem{}).SignalProcess(command.Process.Pid, SignalKill)
		_ = command.Wait()
	})
	deadline := time.Now().Add(time.Second)
	for !(defaultSystem{}).ProcessMatchesCommand(command.Process.Pid, path, config) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !(defaultSystem{}).ProcessMatchesCommand(command.Process.Pid, path, config) {
		arguments, _ := processArguments(command.Process.Pid)
		executable, _ := os.Readlink(procPath(command.Process.Pid, "exe"))
		t.Fatalf("env-shebang process was not matched: arguments=%q executable=%q", arguments, executable)
	}
	if (defaultSystem{}).ProcessMatchesCommand(command.Process.Pid, path, config+"-other") {
		t.Fatal("process matched a different configuration snapshot")
	}
}

func TestStartKMonadMapsMissingCommand(t *testing.T) {
	_, err := (defaultSystem{}).StartKMonad(filepath.Join(t.TempDir(), "missing-kmonad"), nil, nil, nil)
	if !errors.Is(err, ErrCommandNotFound) {
		t.Fatalf("StartKMonad(missing command) error = %v, want ErrCommandNotFound", err)
	}
}
