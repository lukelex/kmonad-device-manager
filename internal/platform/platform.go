// Package platform defines the operating-system boundary used by the manager.
// Core supervision code depends on System rather than Linux process, device,
// locking, or service-manager primitives.
package platform

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"time"
)

var (
	ErrLockHeld    = errors.New("another manager instance is already running")
	ErrProcessGone = errors.New("process no longer exists")
)

type Signal uint8

const (
	SignalInterrupt Signal = iota + 1
	SignalTerminate
	SignalKill
)

type Lock interface {
	Close() error
}

// APIConnection is an authenticated, same-user local API connection. Platform
// implementations must reject unauthenticated peers before returning it.
type APIConnection interface {
	io.Reader
	io.Writer
	io.Closer
}

type APIListener interface {
	Accept() (APIConnection, error)
	Close() error
}

type ProcessHandle interface {
	Close() error
	processHandle()
}

type ProcessInfo struct {
	Handle    ProcessHandle
	StartTick uint64
	GroupID   int
}

// KeyboardDevice contains platform discovery metadata. Identity and NodePath
// are private to the manager and must never be exposed directly to API or CLI
// clients.
type KeyboardDevice struct {
	Identity          string
	FallbackIdentity  string
	IdentityStability string
	NodePath          string
	Availability      DeviceAvailability
	DisplayName       string
	Vendor            string
	Product           string
	Serial            string
}

type DeviceAvailability string

const (
	DeviceConnected    DeviceAvailability = "connected"
	DeviceDisconnected DeviceAvailability = "disconnected"
	DeviceInaccessible DeviceAvailability = "inaccessible"
	DeviceUnsupported  DeviceAvailability = "unsupported"
)

// KeypressObserver waits for the next non-repeat key press on one input node.
// The node path is manager-private and must not be exposed to clients.
type KeypressObserver interface {
	WaitForKeypress(context.Context) error
}

// System is the complete OS-facing surface used by manager core code. New
// platform-dependent behavior belongs behind this interface and its per-OS
// implementations, never in reconciliation or API/domain code.
type System interface {
	RuntimeDir() (string, error)
	AcquireLock() (Lock, string, error)
	APISocketPath() (string, error)
	DialAPISocket(path string) (APIConnection, error)

	DeviceAvailability(path string) DeviceAvailability
	UinputReady(path string) bool
	UinputDevice() string
	UinputModuleLoaded() bool
	WorldWritable(path string) (bool, error)
	DeviceID(path string) (string, error)
	FileSignature(path string) (string, error)
	InGroup(name string) bool
	ListKeyboards() ([]KeyboardDevice, error)
	KeypressObserver(path string) (KeypressObserver, error)
	RenderKMonadInput(path string) (string, error)

	ConfigureChild(command *exec.Cmd)
	TerminationSignals() []os.Signal
	ManagerProcessExists(pid int) bool
	PIDExists(pid int) bool
	ProcessStartTime(pid int) uint64
	ProcessState(pid int) string
	ProcessCommandLine(pid int) string
	ProcessMatchesCommand(pid int, command, config string) bool
	StartedProcess(pid int) ProcessInfo
	SignalProcessGroup(groupID int, signal Signal) error
	SignalProcessHandle(handle ProcessHandle, signal Signal) error
	SignalProcess(pid int, signal Signal) error
	ConfigureCgroup(root, name string, pid int, memoryMax, cpuMax string) (string, error)
	CleanupCgroup(path string) error
	UserServiceStatus(name string) (available, enabled, active bool)
	ListenAPISocket(path string) (APIListener, error)

	NotifyService(message string)
	WatchdogInterval() time.Duration
}

func Default() System {
	return defaultSystem{}
}
