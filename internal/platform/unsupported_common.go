//go:build !linux

package platform

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// unsupportedSystem provides the explicit unavailable behavior shared by
// non-Linux platform descriptors. Platform-specific files identify the host,
// but must not advertise native functionality before implementing its complete
// discovery, rendering, supervision, diagnostics, and integration contract.
type unsupportedSystem struct{}

func (unsupportedSystem) Supported() bool             { return false }
func (unsupportedSystem) PlatformVersion() string     { return "" }
func (unsupportedSystem) BackendVersion() string      { return "" }
func (unsupportedSystem) KMonadAvailable(string) bool { return false }
func (unsupportedSystem) KMonadVersion(context.Context, string) (string, error) {
	return "", unsupported()
}
func (unsupportedSystem) StartKMonad(string, []string, io.Writer, io.Writer) (ChildProcess, error) {
	return nil, unsupported()
}
func (unsupportedSystem) ConfigureChild(*exec.Cmd) {}

type unsupportedLock struct{}

func (unsupportedLock) Close() error { return nil }

type unsupportedProcessHandle struct{}

func (unsupportedProcessHandle) Close() error   { return nil }
func (unsupportedProcessHandle) processHandle() {}

func unsupported() error { return fmt.Errorf("this platform is unsupported") }

func (unsupportedSystem) RuntimeDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "kmonad-device-manager"), nil
}
func (unsupportedSystem) StateDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "kmonad-device-manager"), nil
}
func (unsupportedSystem) AcquireLock() (Lock, string, error)           { return nil, "", unsupported() }
func (unsupportedSystem) APISocketPath() (string, error)               { return "", unsupported() }
func (unsupportedSystem) DialAPISocket(string) (APIConnection, error)  { return nil, unsupported() }
func (unsupportedSystem) DeviceAvailability(string) DeviceAvailability { return DeviceUnsupported }
func (unsupportedSystem) UinputReady(string) bool                      { return false }
func (unsupportedSystem) UinputDevice() string                         { return "" }
func (unsupportedSystem) UinputModuleLoaded() bool                     { return false }
func (unsupportedSystem) WorldWritable(string) (bool, error)           { return false, unsupported() }
func (unsupportedSystem) DeviceID(string) (string, error)              { return "", unsupported() }
func (unsupportedSystem) FileSignature(string) (string, error) {
	return "", unsupported()
}
func (unsupportedSystem) InGroup(string) bool                      { return false }
func (unsupportedSystem) ListKeyboards() ([]KeyboardDevice, error) { return nil, unsupported() }
func (unsupportedSystem) KeypressObserver(string) (KeypressObserver, error) {
	return nil, unsupported()
}
func (unsupportedSystem) RenderKMonadInput(string) (string, error)        { return "", unsupported() }
func (unsupportedSystem) TerminationSignals() []os.Signal                 { return []os.Signal{os.Interrupt} }
func (unsupportedSystem) ManagerProcessExists(int) bool                   { return false }
func (unsupportedSystem) PIDExists(int) bool                              { return false }
func (unsupportedSystem) ProcessStartTime(int) uint64                     { return 0 }
func (unsupportedSystem) ProcessState(int) string                         { return "" }
func (unsupportedSystem) ProcessCommandLine(int) string                   { return "" }
func (unsupportedSystem) ProcessMatchesCommand(int, string, string) bool  { return false }
func (unsupportedSystem) StartedProcess(int) ProcessInfo                  { return ProcessInfo{} }
func (unsupportedSystem) SignalProcessGroup(int, Signal) error            { return unsupported() }
func (unsupportedSystem) SignalProcessHandle(ProcessHandle, Signal) error { return unsupported() }
func (unsupportedSystem) SignalProcess(int, Signal) error                 { return unsupported() }
func (unsupportedSystem) ConfigureCgroup(string, string, int, string, string) (string, error) {
	return "", unsupported()
}
func (unsupportedSystem) CleanupCgroup(string) error { return unsupported() }
func (unsupportedSystem) UserServiceStatus(string) (bool, bool, bool) {
	return false, false, false
}
func (unsupportedSystem) ListenAPISocket(string) (APIListener, error) { return nil, unsupported() }
func (unsupportedSystem) NotifyService(string)                        {}
func (unsupportedSystem) WatchdogInterval() time.Duration             { return 0 }

type unsupportedKeypressObserver struct{}

func (unsupportedKeypressObserver) WaitForKeypress(context.Context) error { return unsupported() }
