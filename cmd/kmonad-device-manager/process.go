package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func processExists(pid int) bool {
	if pid <= 0 {
		return false
	}
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	return err == nil && strings.Contains(string(data), "kmonad-device-manager")
}

func processStateCode(pid int) string {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return ""
	}
	end := strings.LastIndex(string(data), ") ")
	if end < 0 {
		return ""
	}
	fields := strings.Fields(string(data)[end+2:])
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func pidExists(pid int) bool {
	if pid <= 0 {
		return false
	}
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return false
	}
	end := strings.LastIndex(string(data), ") ")
	if end < 0 {
		return false
	}
	fields := strings.Fields(string(data)[end+2:])
	return len(fields) > 0 && fields[0] != "Z"
}

func processStartTime(pid int) uint64 {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0
	}
	end := strings.LastIndex(string(data), ") ")
	if end < 0 {
		return 0
	}
	fields := strings.Fields(string(data)[end+2:])
	if len(fields) <= 19 {
		return 0
	}
	value, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return 0
	}
	return value
}

func processCommandLine(pid int) string {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return ""
	}
	return string(data)
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
		if config.ProcessID == 0 || config.ProcessStart == 0 {
			continue
		}
		if processStartTime(config.ProcessID) != config.ProcessStart {
			continue
		}
		commandLine := processCommandLine(config.ProcessID)
		if commandLine == "" || !strings.Contains(commandLine, config.Name) || !strings.Contains(commandLine, filepath.Base(kmonadCommand)) {
			continue
		}
		signalOwnedProcessID(config.ProcessID, config.ProcessStart, syscall.SIGTERM)
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) && pidExists(config.ProcessID) {
			time.Sleep(25 * time.Millisecond)
		}
		if pidExists(config.ProcessID) {
			signalOwnedProcessID(config.ProcessID, config.ProcessStart, syscall.SIGKILL)
		}
	}
	_ = os.Remove(statusPath)
}

func signalProcessID(pid int, signal syscall.Signal) {
	signalOwnedProcessID(pid, 0, signal)
}

func signalOwnedProcessID(pid int, startTick uint64, signal syscall.Signal) {
	if pid <= 0 || (startTick != 0 && processStartTime(pid) != startTick) {
		return
	}
	process := &processState{
		cmd:       &exec.Cmd{Process: &os.Process{Pid: pid}},
		pidfd:     openProcessFD(pid),
		startTick: startTick,
	}
	defer closeProcessFD(process)
	if startTick != 0 && processStartTime(pid) != startTick {
		return
	}
	signalProcess(process, signal)
}

func openProcessFD(pid int) *os.File {
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return nil
	}
	return os.NewFile(uintptr(fd), "pidfd")
}

func signalProcessFD(file *os.File, signal syscall.Signal) error {
	if file == nil {
		return syscall.EBADF
	}
	return unix.PidfdSendSignal(int(file.Fd()), unix.Signal(signal), nil, 0)
}

func closeProcessFD(process *processState) {
	if process == nil || process.pidfd == nil {
		return
	}
	_ = process.pidfd.Close()
	process.pidfd = nil
}

func inGroup(name string) bool {
	group, err := user.LookupGroup(name)
	if err != nil {
		return false
	}
	wanted, err := strconv.Atoi(group.Gid)
	if err != nil {
		return false
	}
	groups, err := os.Getgroups()
	if err != nil {
		return false
	}
	for _, gid := range groups {
		if gid == wanted {
			return true
		}
	}
	return false
}
