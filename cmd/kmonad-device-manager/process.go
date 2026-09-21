package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
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

func processGroupForPID(pid int) int {
	if pid <= 0 {
		return 0
	}
	group, err := syscall.Getpgid(pid)
	if err != nil {
		return 0
	}
	return group
}

func newProcessState(cmd *exec.Cmd) *processState {
	pid := cmd.Process.Pid
	return &processState{
		cmd:            cmd,
		pidfd:          openProcessFD(pid),
		startTick:      processStartTime(pid),
		processGroupID: processGroupForPID(pid),
	}
}

func processCommandLine(pid int) string {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return ""
	}
	return string(data)
}

func processArguments(pid int) ([]string, error) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("process has no command arguments")
	}
	parts := strings.Split(string(data), "\x00")
	if parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	if len(parts) == 0 {
		return nil, fmt.Errorf("process has no command arguments")
	}
	return parts, nil
}

func processExecutable(pid int) (string, error) {
	path, err := os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "exe"))
	if err != nil {
		return "", err
	}
	return resolvePath(path)
}

func resolveExecutable(command string) (string, error) {
	path, err := exec.LookPath(command)
	if err != nil {
		return "", err
	}
	return resolvePath(path)
}

func resolvePath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

func samePath(left, right string) bool {
	if left == right {
		return true
	}
	resolvedLeft, leftErr := resolvePath(left)
	resolvedRight, rightErr := resolvePath(right)
	return leftErr == nil && rightErr == nil && resolvedLeft == resolvedRight
}

func commandArgumentMatches(actual, command, resolvedCommand string) bool {
	return actual == command || actual == resolvedCommand || samePath(actual, resolvedCommand)
}

func shebangArguments(path string) ([]string, bool) {
	file, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(bufio.NewReader(file), 256))
	if err != nil {
		return nil, false
	}
	line := strings.SplitN(string(data), "\n", 2)[0]
	if !strings.HasPrefix(line, "#!") {
		return nil, false
	}
	fields := strings.Fields(strings.TrimSpace(strings.TrimPrefix(line, "#!")))
	return fields, len(fields) > 0
}

func normalizedInterpreter(arguments []string) ([]string, string, bool) {
	if len(arguments) == 0 {
		return nil, "", false
	}
	if filepath.Base(arguments[0]) == "env" {
		for _, argument := range arguments[1:] {
			if strings.HasPrefix(argument, "-") {
				continue
			}
			resolved, err := resolveExecutable(argument)
			if err != nil {
				return nil, "", false
			}
			return []string{argument}, resolved, true
		}
		return nil, "", false
	}
	resolved, err := resolvePath(arguments[0])
	if err != nil {
		return nil, "", false
	}
	return arguments, resolved, true
}

func processMatchesCommand(pid int, command, config string) bool {
	arguments, err := processArguments(pid)
	if err != nil {
		return false
	}
	resolvedCommand, err := resolveExecutable(command)
	if err != nil {
		return false
	}
	actualExecutable, err := processExecutable(pid)
	if err != nil {
		return false
	}

	if len(arguments) == 2 && arguments[1] == config && commandArgumentMatches(arguments[0], command, resolvedCommand) {
		return actualExecutable == resolvedCommand
	}

	interpreter, isScript := shebangArguments(resolvedCommand)
	if !isScript {
		return false
	}
	interpreter, resolvedInterpreter, ok := normalizedInterpreter(interpreter)
	if !ok || len(arguments) != len(interpreter)+2 || arguments[len(arguments)-1] != config {
		return false
	}
	for index, expected := range interpreter {
		if index == 0 {
			if !samePath(arguments[index], expected) {
				return false
			}
			continue
		}
		if arguments[index] != expected {
			return false
		}
	}
	commandIndex := len(interpreter)
	if !commandArgumentMatches(arguments[commandIndex], command, resolvedCommand) {
		return false
	}
	return samePath(arguments[0], interpreter[0]) && actualExecutable == resolvedInterpreter
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
		configPath := config.Name
		if status.ConfigDir != "" {
			configPath = filepath.Join(status.ConfigDir, config.Name)
		}
		if !processMatchesCommand(config.ProcessID, kmonadCommand, configPath) {
			continue
		}
		signalOwnedProcessIDWithGroup(config.ProcessID, config.ProcessStart, config.ProcessGroupID, syscall.SIGTERM)
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) && pidExists(config.ProcessID) {
			time.Sleep(25 * time.Millisecond)
		}
		if pidExists(config.ProcessID) {
			signalOwnedProcessIDWithGroup(config.ProcessID, config.ProcessStart, config.ProcessGroupID, syscall.SIGKILL)
		}
	}
	_ = os.Remove(statusPath)
}

func signalProcessID(pid int, signal syscall.Signal) {
	signalOwnedProcessID(pid, 0, signal)
}

func signalOwnedProcessID(pid int, startTick uint64, signal syscall.Signal) {
	signalOwnedProcessIDWithGroup(pid, startTick, 0, signal)
}

func signalOwnedProcessIDWithGroup(pid int, startTick uint64, groupID int, signal syscall.Signal) {
	if pid <= 0 || (startTick != 0 && processStartTime(pid) != startTick) {
		return
	}
	process := &processState{
		cmd:            &exec.Cmd{Process: &os.Process{Pid: pid}},
		pidfd:          openProcessFD(pid),
		startTick:      startTick,
		processGroupID: groupID,
	}
	if process.processGroupID == 0 {
		process.processGroupID = processGroupForPID(pid)
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
