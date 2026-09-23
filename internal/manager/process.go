package manager

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/lukelex/kmonad-device-manager/internal/platform"
)

// The manager's process lifecycle is platform-neutral. Kernel process identity,
// handles, process groups, and parent-death details live behind internal/platform.
func processExists(pid int) bool {
	return host.ManagerProcessExists(pid)
}

func processStateCode(pid int) string {
	return host.ProcessState(pid)
}

func pidExists(pid int) bool {
	return host.PIDExists(pid)
}

func processStartTime(pid int) uint64 {
	return host.ProcessStartTime(pid)
}

func processCommandLine(pid int) string {
	return host.ProcessCommandLine(pid)
}

func processMatchesCommand(pid int, command, config string) bool {
	return host.ProcessMatchesCommand(pid, command, config)
}

func inGroup(name string) bool {
	return host.InGroup(name)
}

func newProcessState(child platform.ChildProcess) *processState {
	pid := child.PID()
	info := host.StartedProcess(pid)
	return &processState{
		child:          child,
		pid:            pid,
		pidfd:          info.Handle,
		startTick:      info.StartTick,
		processGroupID: info.GroupID,
	}
}

func recoverOwnedProcesses(statusPath, kmonadCommand string) {
	data, err := os.ReadFile(statusPath)
	if err != nil {
		return
	}
	var status statusFile
	if err := json.Unmarshal(data, &status); err != nil {
		_ = os.Remove(statusPath)
		return
	}
	for _, config := range status.Configurations {
		if config.ProcessID == 0 || config.ProcessStart == 0 || processStartTime(config.ProcessID) != config.ProcessStart {
			continue
		}
		configPath := config.Name
		if status.ConfigDir != "" {
			configPath = filepath.Join(status.ConfigDir, config.Name)
		}
		launchPath := config.LaunchPath
		if launchPath == "" {
			launchPath = configPath
		}
		if !processMatchesCommand(config.ProcessID, kmonadCommand, launchPath) {
			_ = removeConfigSnapshot(config.LaunchPath)
			continue
		}
		signalOwnedProcessIDWithGroup(config.ProcessID, config.ProcessStart, config.ProcessGroupID, platform.SignalTerminate)
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) && pidExists(config.ProcessID) {
			time.Sleep(25 * time.Millisecond)
		}
		if pidExists(config.ProcessID) {
			signalOwnedProcessIDWithGroup(config.ProcessID, config.ProcessStart, config.ProcessGroupID, platform.SignalKill)
		}
		if !pidExists(config.ProcessID) {
			_ = removeConfigSnapshot(config.LaunchPath)
		}
	}
	_ = os.Remove(statusPath)
}

func signalProcessID(pid int, signal platform.Signal) {
	signalOwnedProcessID(pid, 0, signal)
}

func signalOwnedProcessID(pid int, startTick uint64, signal platform.Signal) {
	signalOwnedProcessIDWithGroup(pid, startTick, 0, signal)
}

func signalOwnedProcessIDWithGroup(pid int, startTick uint64, groupID int, signal platform.Signal) {
	if pid <= 0 || (startTick != 0 && processStartTime(pid) != startTick) {
		return
	}
	info := host.StartedProcess(pid)
	process := &processState{pid: pid, pidfd: info.Handle, startTick: info.StartTick, processGroupID: info.GroupID}
	process.startTick = startTick
	if groupID != 0 {
		process.processGroupID = groupID
	}
	defer closeProcessFD(process)
	if startTick != 0 && processStartTime(pid) != startTick {
		return
	}
	signalProcess(process, signal)
}

func closeProcessFD(process *processState) {
	if process == nil || process.pidfd == nil {
		return
	}
	_ = process.pidfd.Close()
	process.pidfd = nil
}

func signalProcessHandle(handle platform.ProcessHandle, signal platform.Signal) error {
	if handle == nil {
		return errors.New("process handle is unavailable")
	}
	return host.SignalProcessHandle(handle, signal)
}
