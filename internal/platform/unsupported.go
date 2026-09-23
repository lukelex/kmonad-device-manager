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

// defaultSystem keeps non-Linux builds type-safe while reporting unavailable
// Linux service functionality through the shared System interface.
type defaultSystem struct{}

func (defaultSystem) Supported() bool             { return false }
func (defaultSystem) Platform() string            { return "unsupported" }
func (defaultSystem) PlatformVersion() string     { return "" }
func (defaultSystem) Backend() string             { return "unsupported" }
func (defaultSystem) BackendVersion() string      { return "" }
func (defaultSystem) KMonadAvailable(string) bool { return false }
func (defaultSystem) KMonadVersion(context.Context, string) (string, error) {
	return "", unsupported()
}
func (defaultSystem) StartKMonad(string, []string, io.Writer, io.Writer) (ChildProcess, error) {
	return nil, unsupported()
}
func (defaultSystem) ConfigureChild(*exec.Cmd) {}

type unsupportedLock struct{}

func (unsupportedLock) Close() error { return nil }

type unsupportedProcessHandle struct{}

func (unsupportedProcessHandle) Close() error   { return nil }
func (unsupportedProcessHandle) processHandle() {}

func unsupported() error { return fmt.Errorf("this platform is unsupported") }

func (defaultSystem) RuntimeDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "kmonad-device-manager"), nil
}
func (defaultSystem) StateDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "kmonad-device-manager"), nil
}
func (defaultSystem) AcquireLock() (Lock, string, error)           { return nil, "", unsupported() }
func (defaultSystem) APISocketPath() (string, error)               { return "", unsupported() }
func (defaultSystem) DialAPISocket(string) (APIConnection, error)  { return nil, unsupported() }
func (defaultSystem) DeviceAvailability(string) DeviceAvailability { return DeviceUnsupported }
func (defaultSystem) UinputReady(string) bool                      { return false }
func (defaultSystem) UinputDevice() string                         { return "" }
func (defaultSystem) UinputModuleLoaded() bool                     { return false }
func (defaultSystem) WorldWritable(string) (bool, error)           { return false, unsupported() }
func (defaultSystem) DeviceID(string) (string, error)              { return "", unsupported() }
func (defaultSystem) FileSignature(string) (string, error) {
	return "", unsupported()
}
func (defaultSystem) InGroup(string) bool                               { return false }
func (defaultSystem) ListKeyboards() ([]KeyboardDevice, error)          { return nil, unsupported() }
func (defaultSystem) KeypressObserver(string) (KeypressObserver, error) { return nil, unsupported() }
func (defaultSystem) RenderKMonadInput(string) (string, error)          { return "", unsupported() }
func (defaultSystem) TerminationSignals() []os.Signal                   { return []os.Signal{os.Interrupt} }
func (defaultSystem) ManagerProcessExists(int) bool                     { return false }
func (defaultSystem) PIDExists(int) bool                                { return false }
func (defaultSystem) ProcessStartTime(int) uint64                       { return 0 }
func (defaultSystem) ProcessState(int) string                           { return "" }
func (defaultSystem) ProcessCommandLine(int) string                     { return "" }
func (defaultSystem) ProcessMatchesCommand(int, string, string) bool    { return false }
func (defaultSystem) StartedProcess(int) ProcessInfo                    { return ProcessInfo{} }
func (defaultSystem) SignalProcessGroup(int, Signal) error              { return unsupported() }
func (defaultSystem) SignalProcessHandle(ProcessHandle, Signal) error   { return unsupported() }
func (defaultSystem) SignalProcess(int, Signal) error                   { return unsupported() }
func (defaultSystem) ConfigureCgroup(string, string, int, string, string) (string, error) {
	return "", unsupported()
}
func (defaultSystem) CleanupCgroup(string) error { return unsupported() }
func (defaultSystem) UserServiceStatus(string) (bool, bool, bool) {
	return false, false, false
}
func (defaultSystem) ListenAPISocket(string) (APIListener, error) { return nil, unsupported() }
func (defaultSystem) NotifyService(string)                        {}
func (defaultSystem) WatchdogInterval() time.Duration             { return 0 }

type unsupportedKeypressObserver struct{}

func (unsupportedKeypressObserver) WaitForKeypress(context.Context) error { return unsupported() }
