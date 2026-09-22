//go:build linux

package platform

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

type defaultSystem struct{}

var (
	cgroupMkdir     = os.Mkdir
	cgroupStat      = os.Stat
	cgroupWriteFile = os.WriteFile
)

var (
	inputSysfsRoot                    = "/sys/class/input"
	inputDeviceRoot                   = "/dev/input"
	deviceStat                        = os.Stat
	openDevice                        = os.Open
	inputBackend    linuxInputBackend = sysfsInputBackend{}
)

type linuxLock struct{ file *os.File }

type linuxAPIListener struct {
	listener *net.UnixListener
	path     string
}

type linuxAPIConnection struct{ *net.UnixConn }

type linuxKeypressObserver struct{ file *os.File }

// linuxInputBackend separates sysfs discovery and evdev observation from the
// rest of the Linux platform implementation. Tests replace it with fixtures so
// they never need a host keyboard or readable /dev/input node.
type linuxInputBackend interface {
	ListKeyboards() ([]KeyboardDevice, error)
	KeypressObserver(path string) (KeypressObserver, error)
}

type sysfsInputBackend struct{}

func (lock *linuxLock) Close() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	_ = syscall.Flock(int(lock.file.Fd()), syscall.LOCK_UN)
	err := lock.file.Close()
	lock.file = nil
	return err
}

func (defaultSystem) RuntimeDir() (string, error) {
	if base := os.Getenv("XDG_RUNTIME_DIR"); base != "" {
		return base, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "kmonad-device-manager"), nil
}

func (defaultSystem) StateDir() (string, error) {
	if base := os.Getenv("STATE_DIRECTORY"); base != "" {
		return base, nil
	}
	if base := os.Getenv("XDG_STATE_HOME"); base != "" {
		return filepath.Join(base, "kmonad-device-manager"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "kmonad-device-manager"), nil
}

func (system defaultSystem) AcquireLock() (Lock, string, error) {
	base, err := system.RuntimeDir()
	if err != nil {
		return nil, "", err
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		return nil, "", err
	}
	path := filepath.Join(base, "kmonad-device-manager.lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, "", err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, "", ErrLockHeld
		}
		return nil, "", err
	}
	return &linuxLock{file: file}, path, nil
}

func (system defaultSystem) APISocketPath() (string, error) {
	base, err := system.RuntimeDir()
	if err != nil {
		return "", err
	}
	if os.Getenv("XDG_RUNTIME_DIR") == "" {
		return filepath.Join(base, "api.sock"), nil
	}
	info, err := os.Stat(base)
	if err != nil {
		return "", fmt.Errorf("inspect runtime directory: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("runtime directory is not private to the manager user")
	}
	directory := filepath.Join(base, "kmonad-device-manager")
	if err := os.Mkdir(directory, 0o700); err != nil && !os.IsExist(err) {
		return "", fmt.Errorf("create API directory: %w", err)
	}
	info, err = os.Lstat(directory)
	if err != nil {
		return "", fmt.Errorf("inspect API directory: %w", err)
	}
	stat, ok = info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || !ok || stat.Uid != uint32(os.Geteuid()) {
		return "", fmt.Errorf("API directory is not owned by the manager user")
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return "", fmt.Errorf("secure API directory: %w", err)
	}
	return filepath.Join(directory, "api.sock"), nil
}

func (defaultSystem) DialAPISocket(path string) (APIConnection, error) {
	connection, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	return &linuxAPIConnection{UnixConn: connection}, nil
}

func (defaultSystem) DeviceAvailability(path string) DeviceAvailability {
	return deviceAvailability(path)
}

func deviceAvailability(path string) DeviceAvailability {
	info, err := deviceStat(path)
	if os.IsNotExist(err) {
		return DeviceDisconnected
	}
	if err != nil {
		return DeviceInaccessible
	}
	if info.Mode()&os.ModeCharDevice == 0 {
		return DeviceUnsupported
	}
	file, err := openDevice(path)
	if err != nil {
		return DeviceInaccessible
	}
	if file.Close() != nil {
		return DeviceInaccessible
	}
	return DeviceConnected
}

func (defaultSystem) UinputReady(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return false
	}
	return file.Close() == nil
}

func (defaultSystem) UinputDevice() string { return "/dev/uinput" }

func (defaultSystem) UinputModuleLoaded() bool {
	_, err := os.Stat("/sys/module/uinput")
	return err == nil
}

func (defaultSystem) WorldWritable(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	return info.Mode().Perm()&0o002 != 0, nil
}

func (defaultSystem) DeviceID(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeCharDevice == 0 {
		return "", fmt.Errorf("not a character device")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fmt.Errorf("unsupported device stat")
	}
	return strconv.FormatUint(uint64(stat.Rdev), 10), nil
}

func (defaultSystem) FileSignature(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fmt.Errorf("unsupported file stat")
	}
	return fmt.Sprintf("%d:%d:%d:%d:%d", stat.Dev, stat.Ino, info.ModTime().UnixNano(), info.Size(), info.Mode()), nil
}

func (defaultSystem) InGroup(name string) bool {
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
	for _, groupID := range groups {
		if groupID == wanted {
			return true
		}
	}
	return false
}

func (defaultSystem) ListKeyboards() ([]KeyboardDevice, error) {
	return inputBackend.ListKeyboards()
}

func (sysfsInputBackend) ListKeyboards() ([]KeyboardDevice, error) {
	entries, err := os.ReadDir(inputSysfsRoot)
	if err != nil {
		return nil, err
	}
	devices := make([]KeyboardDevice, 0)
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "event") {
			continue
		}
		path := filepath.Join(inputSysfsRoot, entry.Name(), "device")
		keys, err := os.ReadFile(filepath.Join(path, "capabilities", "key"))
		if err != nil || !keyboardCapabilities(string(keys)) {
			continue
		}
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			continue
		}
		device := KeyboardDevice{
			NodePath:     filepath.Join(inputDeviceRoot, entry.Name()),
			Availability: deviceAvailability(filepath.Join(inputDeviceRoot, entry.Name())),
			DisplayName:  strings.TrimSpace(readOptionalFile(filepath.Join(path, "name"))),
		}
		device.Vendor, device.Product, device.Serial = inputMetadata(resolved)
		device.FallbackIdentity = "topology:" + resolved
		if device.Serial != "" {
			device.Identity = "serial:" + device.Vendor + ":" + device.Product + ":" + device.Serial
			device.IdentityStability = "serial"
		} else {
			device.Identity = device.FallbackIdentity
			device.IdentityStability = "topology"
		}
		if device.DisplayName == "" {
			device.DisplayName = entry.Name()
		}
		devices = append(devices, device)
	}
	sort.Slice(devices, func(i, j int) bool { return devices[i].Identity < devices[j].Identity })
	return devices, nil
}

func (defaultSystem) KeypressObserver(path string) (KeypressObserver, error) {
	return inputBackend.KeypressObserver(path)
}

func (sysfsInputBackend) KeypressObserver(path string) (KeypressObserver, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	return &linuxKeypressObserver{file: file}, nil
}

func (defaultSystem) RenderKMonadInput(path string) (string, error) {
	if availability := deviceAvailability(path); availability != DeviceConnected {
		return "", fmt.Errorf("input device is %s", availability)
	}
	return "input (device-file " + strconv.Quote(path) + ")", nil
}

func (observer *linuxKeypressObserver) WaitForKeypress(ctx context.Context) error {
	if observer == nil || observer.file == nil {
		return ErrProcessGone
	}
	defer observer.file.Close()
	fd := int(observer.file.Fd())
	if err := unix.SetNonblock(fd, true); err != nil {
		return err
	}
	timevalSize := int(unsafe.Sizeof(unix.Timeval{}))
	eventSize := timevalSize + 8
	buffer := make([]byte, eventSize*16)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		_, err := unix.Poll(poll, 100)
		if err != nil {
			if err == unix.EINTR {
				continue
			}
			return err
		}
		if poll[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 {
			return ErrProcessGone
		}
		if poll[0].Revents&unix.POLLIN == 0 {
			continue
		}
		count, err := unix.Read(fd, buffer)
		if err != nil {
			if err == unix.EAGAIN || err == unix.EWOULDBLOCK || err == unix.EINTR {
				continue
			}
			return err
		}
		if containsKeypress(buffer[:count], timevalSize) {
			return nil
		}
	}
}

func containsKeypress(data []byte, timevalSize int) bool {
	eventSize := timevalSize + 8
	for offset := 0; offset+eventSize <= len(data); offset += eventSize {
		event := data[offset : offset+eventSize]
		if binary.NativeEndian.Uint16(event[timevalSize:]) == unix.EV_KEY && binary.NativeEndian.Uint32(event[timevalSize+4:]) == 1 {
			return true
		}
	}
	return false
}

func keyboardCapabilities(value string) bool {
	return inputCapabilitySet(value, 30) && inputCapabilitySet(value, 44)
}

func inputCapabilitySet(value string, code int) bool {
	words := strings.Fields(value)
	word := code / 32
	if word >= len(words) {
		return false
	}
	parsed, err := strconv.ParseUint(words[len(words)-1-word], 16, 64)
	return err == nil && parsed&(uint64(1)<<uint(code%32)) != 0
}

func inputMetadata(path string) (vendor, product, serial string) {
	for current := path; current != "/" && current != "."; current = filepath.Dir(current) {
		if vendor == "" {
			vendor = strings.TrimSpace(readOptionalFile(filepath.Join(current, "id", "vendor")))
		}
		if product == "" {
			product = strings.TrimSpace(readOptionalFile(filepath.Join(current, "id", "product")))
		}
		if serial == "" {
			serial = strings.TrimSpace(readOptionalFile(filepath.Join(current, "serial")))
		}
		if vendor != "" || product != "" || serial != "" {
			return
		}
	}
	return
}

func readOptionalFile(path string) string {
	data, _ := os.ReadFile(path)
	return string(data)
}

func (defaultSystem) ConfigureChild(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
}

func (defaultSystem) TerminationSignals() []os.Signal {
	return []os.Signal{syscall.SIGINT, syscall.SIGTERM}
}

func procPath(pid int, name string) string {
	return filepath.Join("/proc", strconv.Itoa(pid), name)
}

func processArguments(pid int) ([]string, error) {
	data, err := os.ReadFile(procPath(pid, "cmdline"))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("process has no command arguments")
	}
	arguments := strings.Split(string(data), "\x00")
	if arguments[len(arguments)-1] == "" {
		arguments = arguments[:len(arguments)-1]
	}
	if len(arguments) == 0 {
		return nil, fmt.Errorf("process has no command arguments")
	}
	return arguments, nil
}

func (defaultSystem) ManagerProcessExists(pid int) bool {
	if pid <= 0 {
		return false
	}
	data, err := os.ReadFile(procPath(pid, "cmdline"))
	return err == nil && strings.Contains(string(data), "kmonad-device-manager")
}

func (defaultSystem) PIDExists(pid int) bool {
	if pid <= 0 {
		return false
	}
	return (defaultSystem{}).ProcessState(pid) != "" && (defaultSystem{}).ProcessState(pid) != "Z"
}

func (defaultSystem) ProcessStartTime(pid int) uint64 {
	data, err := os.ReadFile(procPath(pid, "stat"))
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

func (defaultSystem) ProcessState(pid int) string {
	data, err := os.ReadFile(procPath(pid, "stat"))
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

func (defaultSystem) ProcessCommandLine(pid int) string {
	data, err := os.ReadFile(procPath(pid, "cmdline"))
	if err != nil {
		return ""
	}
	return string(data)
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

func resolveExecutable(command string) (string, error) {
	path, err := exec.LookPath(command)
	if err != nil {
		return "", err
	}
	return resolvePath(path)
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

func (defaultSystem) ProcessMatchesCommand(pid int, command, config string) bool {
	arguments, err := processArguments(pid)
	if err != nil {
		return false
	}
	resolvedCommand, err := resolveExecutable(command)
	if err != nil {
		return false
	}
	actualExecutable, err := os.Readlink(procPath(pid, "exe"))
	if err != nil {
		return false
	}
	actualExecutable, err = resolvePath(actualExecutable)
	if err != nil {
		return false
	}
	argumentMatches := func(actual string) bool {
		return actual == command || actual == resolvedCommand || samePath(actual, resolvedCommand)
	}
	if len(arguments) == 2 && arguments[1] == config && argumentMatches(arguments[0]) {
		return actualExecutable == resolvedCommand
	}
	interpreter, script := shebangArguments(resolvedCommand)
	if !script || len(interpreter) == 0 {
		return false
	}
	resolvedInterpreter := ""
	if filepath.Base(interpreter[0]) == "env" {
		for _, argument := range interpreter[1:] {
			if !strings.HasPrefix(argument, "-") {
				resolved, resolveErr := resolveExecutable(argument)
				if resolveErr != nil {
					return false
				}
				interpreter = []string{argument}
				resolvedInterpreter = resolved
				break
			}
		}
	}
	if resolvedInterpreter == "" {
		var resolveErr error
		resolvedInterpreter, resolveErr = resolvePath(interpreter[0])
		if resolveErr != nil {
			return false
		}
	}
	if len(arguments) != len(interpreter)+2 || arguments[len(arguments)-1] != config || !argumentMatches(arguments[len(interpreter)]) {
		return false
	}
	for index, expected := range interpreter {
		if index == 0 {
			if !samePath(arguments[index], expected) {
				return false
			}
		} else if arguments[index] != expected {
			return false
		}
	}
	return actualExecutable == resolvedInterpreter
}

type linuxProcessHandle struct{ file *os.File }

func (*linuxProcessHandle) processHandle() {}

func (handle *linuxProcessHandle) Close() error {
	if handle == nil || handle.file == nil {
		return nil
	}
	err := handle.file.Close()
	handle.file = nil
	return err
}

func (defaultSystem) StartedProcess(pid int) ProcessInfo {
	groupID, _ := syscall.Getpgid(pid)
	info := ProcessInfo{StartTick: (defaultSystem{}).ProcessStartTime(pid), GroupID: groupID}
	if fd, err := unix.PidfdOpen(pid, 0); err == nil {
		info.Handle = &linuxProcessHandle{file: os.NewFile(uintptr(fd), "pidfd")}
	}
	return info
}

func linuxSignal(signal Signal) syscall.Signal {
	switch signal {
	case SignalInterrupt:
		return syscall.SIGINT
	case SignalKill:
		return syscall.SIGKILL
	default:
		return syscall.SIGTERM
	}
}

func (defaultSystem) SignalProcessGroup(groupID int, signal Signal) error {
	if groupID <= 0 {
		return ErrProcessGone
	}
	if err := syscall.Kill(-groupID, linuxSignal(signal)); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return ErrProcessGone
		}
		return err
	}
	return nil
}

func (defaultSystem) SignalProcessHandle(handle ProcessHandle, signal Signal) error {
	pidfd, ok := handle.(*linuxProcessHandle)
	if !ok || pidfd == nil || pidfd.file == nil {
		return fmt.Errorf("invalid process handle")
	}
	if err := unix.PidfdSendSignal(int(pidfd.file.Fd()), unix.Signal(linuxSignal(signal)), nil, 0); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return ErrProcessGone
		}
		return err
	}
	return nil
}

func (defaultSystem) SignalProcess(pid int, signal Signal) error {
	if pid <= 0 {
		return ErrProcessGone
	}
	if err := syscall.Kill(pid, linuxSignal(signal)); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return ErrProcessGone
		}
		return err
	}
	return nil
}

func (defaultSystem) ConfigureCgroup(root, name string, pid int, memoryMax, cpuMax string) (string, error) {
	if root == "" {
		return "", nil
	}
	path := filepath.Join(root, name)
	created := false
	if err := cgroupMkdir(path, 0o755); err != nil {
		if !os.IsExist(err) {
			return "", err
		}
	} else {
		created = true
	}
	completed := false
	defer func() {
		if !completed && created {
			_ = (defaultSystem{}).CleanupCgroup(path)
		}
	}()
	for _, limit := range []struct {
		name  string
		value string
	}{
		{name: "memory.max", value: memoryMax},
		{name: "cpu.max", value: cpuMax},
	} {
		if limit.value == "" {
			continue
		}
		limitPath := filepath.Join(path, limit.name)
		if _, err := cgroupStat(limitPath); err != nil {
			return "", fmt.Errorf("%s is unavailable: %w", limit.name, err)
		}
		if err := cgroupWriteFile(limitPath, []byte(limit.value+"\n"), 0o600); err != nil {
			return "", err
		}
	}
	procs := filepath.Join(path, "cgroup.procs")
	if _, err := cgroupStat(procs); err != nil {
		return "", fmt.Errorf("cgroup.procs is unavailable: %w", err)
	}
	if err := cgroupWriteFile(procs, []byte(strconv.Itoa(pid)+"\n"), 0o600); err != nil {
		return "", err
	}
	completed = true
	return path, nil
}

func (defaultSystem) CleanupCgroup(path string) error {
	if path == "" {
		return nil
	}
	killPath := filepath.Join(path, "cgroup.kill")
	if _, err := os.Stat(killPath); err == nil {
		if err := os.WriteFile(killPath, []byte("1\n"), 0o600); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	deadline := time.Now().Add(100 * time.Millisecond)
	for {
		data, err := os.ReadFile(filepath.Join(path, "cgroup.procs"))
		if os.IsNotExist(err) {
			return os.Remove(path)
		}
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(data)) == "" {
			return os.Remove(path)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("cgroup still contains processes")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (defaultSystem) UserServiceStatus(name string) (available, enabled, active bool) {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return false, false, false
	}
	return true,
		exec.Command("systemctl", "--user", "is-enabled", name).Run() == nil,
		exec.Command("systemctl", "--user", "is-active", name).Run() == nil
}

func (defaultSystem) ListenAPISocket(path string) (APIListener, error) {
	directory := filepath.Dir(path)
	info, err := os.Stat(directory)
	if err != nil {
		return nil, fmt.Errorf("inspect API directory: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return nil, fmt.Errorf("API directory is not owned by the manager user")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("API directory permissions must not grant group or other access")
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("refusing to replace non-socket API path")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != uint32(os.Geteuid()) {
			return nil, fmt.Errorf("refusing to replace API socket not owned by the manager user")
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("remove stale API socket: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect API socket path: %w", err)
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("set API socket permissions: %w", err)
	}
	return &linuxAPIListener{listener: listener, path: path}, nil
}

func (listener *linuxAPIListener) Accept() (APIConnection, error) {
	for {
		connection, err := listener.listener.AcceptUnix()
		if err != nil {
			return nil, err
		}
		peerUID, err := linuxPeerUID(connection)
		if err != nil || peerUID != os.Geteuid() {
			_ = connection.Close()
			continue
		}
		return linuxAPIConnection{UnixConn: connection}, nil
	}
}

func (listener *linuxAPIListener) Close() error {
	if listener == nil || listener.listener == nil {
		return nil
	}
	err := listener.listener.Close()
	if removeErr := os.Remove(listener.path); removeErr != nil && !os.IsNotExist(removeErr) && err == nil {
		err = removeErr
	}
	return err
}

func linuxPeerUID(connection *net.UnixConn) (int, error) {
	raw, err := connection.SyscallConn()
	if err != nil {
		return 0, err
	}
	var peerUID int
	var controlErr error
	err = raw.Control(func(fd uintptr) {
		credentials, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if err != nil {
			controlErr = err
			return
		}
		peerUID = int(credentials.Uid)
	})
	if err != nil {
		return 0, err
	}
	if controlErr != nil {
		return 0, controlErr
	}
	return peerUID, nil
}

func (defaultSystem) NotifyService(message string) {
	socket := os.Getenv("NOTIFY_SOCKET")
	if socket == "" {
		return
	}
	if strings.HasPrefix(socket, "@") {
		socket = "\x00" + socket[1:]
	}
	connection, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: socket, Net: "unixgram"})
	if err != nil {
		return
	}
	defer connection.Close()
	_, _ = connection.Write([]byte(message + "\n"))
}

func (defaultSystem) WatchdogInterval() time.Duration {
	usec, err := strconv.ParseInt(os.Getenv("WATCHDOG_USEC"), 10, 64)
	if err != nil || usec <= 0 || os.Getenv("NOTIFY_SOCKET") == "" {
		return 0
	}
	return time.Duration(usec) * time.Microsecond
}
