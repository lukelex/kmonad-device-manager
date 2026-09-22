// Package platform defines the operating-system boundary used by the manager.
// Core supervision code depends on System rather than Linux process, device,
// locking, or service-manager primitives.
package platform

import (
	"errors"
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

type ProcessHandle interface {
	Close() error
	processHandle()
}

type ProcessInfo struct {
	Handle    ProcessHandle
	StartTick uint64
	GroupID   int
}

// System is the complete OS-facing surface used by manager core code. New
// platform-dependent behavior belongs behind this interface and its per-OS
// implementations, never in reconciliation or API/domain code.
type System interface {
	RuntimeDir() (string, error)
	AcquireLock() (Lock, string, error)

	DeviceReady(path string) bool
	UinputReady(path string) bool
	UinputDevice() string
	UinputModuleLoaded() bool
	WorldWritable(path string) (bool, error)
	DeviceID(path string) (string, error)
	FileSignature(path string) (string, error)
	InGroup(name string) bool

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

	NotifyService(message string)
	WatchdogInterval() time.Duration
}

func Default() System {
	return defaultSystem{}
}
