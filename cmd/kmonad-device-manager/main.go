package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/lukelex/kmonad-device-manager/internal/completions"
)

var version = "0.3.0"

type configToken struct {
	kind  byte
	value string
}

type settings struct {
	configDir       string
	kmonadCommand   string
	pollIntervalRaw string
	stopTimeoutRaw  string
	pollInterval    time.Duration
	stopTimeout     time.Duration
}

type processState struct {
	cmd       *exec.Cmd
	done      chan struct{}
	exitErr   error
	startedAt time.Time
}

type configState struct {
	signature  string
	deviceID   string
	failures   int
	retryAfter time.Time
	process    *processState
}

type manager struct {
	configDir     string
	kmonadCommand string
	stopTimeout   time.Duration
	statusPath    string
	states        map[string]*configState
	duplicates    map[string]string
}

type statusFile struct {
	PID            int            `json:"pid"`
	UpdatedAt      time.Time      `json:"updated_at"`
	ConfigDir      string         `json:"config_dir"`
	Configurations []statusConfig `json:"configurations"`
}

type statusConfig struct {
	Name         string `json:"name"`
	State        string `json:"state"`
	Device       string `json:"device,omitempty"`
	ProcessID    int    `json:"process_id,omitempty"`
	ProcessStart uint64 `json:"process_start,omitempty"`
}

var (
	errLockHeld      = errors.New("another manager instance is already running")
	errConfigChanged = errors.New("configuration changed while it was being read")
)

func main() {
	s := loadSettings()

	switch {
	case len(os.Args) == 2 && os.Args[1] == "--doctor":
		os.Exit(doctor(s))
	case len(os.Args) == 2 && os.Args[1] == "--version":
		fmt.Printf("kmonad-device-manager %s\n", version)
		return
	case len(os.Args) == 2 && os.Args[1] == "--status":
		os.Exit(showStatus())
	case len(os.Args) == 2 && (os.Args[1] == "-h" || os.Args[1] == "--help"):
		fmt.Println("Usage: kmonad-device-manager [--doctor] [--status] [--completion <bash|zsh|fish>] [--version]")
		return
	case len(os.Args) == 3 && os.Args[1] == "--completion":
		output, err := completions.For(os.Args[2])
		if err != nil {
			fmt.Fprintf(os.Stderr, "kmonad-device-manager: %v\n", err)
			os.Exit(2)
		}
		fmt.Print(output)
		return
	case len(os.Args) == 1:
		// Continue into the service.
	default:
		fmt.Fprintf(os.Stderr, "kmonad-device-manager: unknown option: %s\n", os.Args[1])
		os.Exit(2)
	}

	if err := validateSettings(s); err != nil {
		fmt.Fprintf(os.Stderr, "kmonad-device-manager: %v\n", err)
		os.Exit(2)
	}
	if _, err := exec.LookPath(s.kmonadCommand); err != nil {
		fmt.Fprintln(os.Stderr, "kmonad-device-manager: KMonad is not installed or is not on PATH.")
		fmt.Fprintln(os.Stderr, "Install KMonad from https://github.com/kmonad/kmonad, then restart this service.")
		os.Exit(127)
	}

	lock, lockPath, err := acquireLock()
	if err != nil {
		if errors.Is(err, errLockHeld) {
			fmt.Fprintln(os.Stderr, "kmonad-device-manager: another instance is already running.")
		} else {
			fmt.Fprintf(os.Stderr, "kmonad-device-manager: cannot acquire single-instance lock: %v\n", err)
		}
		os.Exit(1)
	}
	defer releaseLock(lock)

	statusPath := filepath.Join(filepath.Dir(lockPath), "status.json")
	recoverOwnedProcesses(statusPath, s.kmonadCommand)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	m := &manager{
		configDir:     s.configDir,
		kmonadCommand: s.kmonadCommand,
		stopTimeout:   s.stopTimeout,
		statusPath:    statusPath,
		states:        make(map[string]*configState),
		duplicates:    make(map[string]string),
	}
	defer m.cleanup()
	m.run(ctx, s.pollInterval)
}

func loadSettings() settings {
	configDir := os.Getenv("KMONAD_CONFIG_DIR")
	if configDir == "" {
		envFile := filepath.Join(configHome(), "kmonad-device-manager", "env")
		if value, err := environmentValue(envFile, "KMONAD_CONFIG_DIR"); err == nil {
			configDir = value
		}
	}
	if configDir == "" {
		configDir = filepath.Join(configHome(), "kmonad")
	}

	s := settings{
		configDir:       configDir,
		kmonadCommand:   valueOr("KMONAD_COMMAND", "kmonad"),
		pollIntervalRaw: valueOr("KMONAD_POLL_INTERVAL", "2"),
		stopTimeoutRaw:  valueOr("KMONAD_STOP_TIMEOUT", "5"),
	}
	s.pollInterval = seconds(s.pollIntervalRaw)
	s.stopTimeout = seconds(s.stopTimeoutRaw)
	return s
}

func configHome() string {
	if value := os.Getenv("XDG_CONFIG_HOME"); value != "" {
		return value
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".config"
	}
	return filepath.Join(home, ".config")
}

func valueOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func seconds(value string) time.Duration {
	n, err := strconv.Atoi(value)
	maxSeconds := int64(^uint64(0)>>1) / int64(time.Second)
	if err != nil || n <= 0 || int64(n) > maxSeconds {
		return 0
	}
	return time.Duration(n) * time.Second
}

func validateSettings(s settings) error {
	if s.pollInterval == 0 {
		return fmt.Errorf("KMONAD_POLL_INTERVAL must be a positive integer")
	}
	if s.stopTimeout == 0 {
		return fmt.Errorf("KMONAD_STOP_TIMEOUT must be a positive integer")
	}
	return nil
}

func environmentValue(path, wanted string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) != wanted {
			continue
		}
		value := strings.TrimSpace(parts[1])
		if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
			value = value[1 : len(value)-1]
		}
		return value, nil
	}
	return "", scanner.Err()
}

func runtimeDir() (string, error) {
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base != "" {
		return base, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "kmonad-device-manager"), nil
}

func acquireLock() (*os.File, string, error) {
	base, err := runtimeDir()
	if err != nil {
		return nil, "", err
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		return nil, "", err
	}
	lockPath := filepath.Join(base, "kmonad-device-manager.lock")
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, "", err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, "", errLockHeld
		}
		return nil, "", err
	}
	return file, lockPath, nil
}

func releaseLock(file *os.File) {
	if file == nil {
		return
	}
	_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	_ = file.Close()
}

func (m *manager) run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		m.reconcile(time.Now())
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (m *manager) reconcile(now time.Time) {
	activeConfigs := make(map[string]bool)
	activeDevices := make(map[string]string)
	stopDeadline := now.Add(m.stopTimeout)
	if unsafe, err := worldWritable(m.configDir); err != nil && !os.IsNotExist(err) {
		logf("cannot inspect configuration directory: %v", err)
		m.stopAll(stopDeadline)
		m.writeStatus()
		return
	} else if unsafe {
		logf("configuration directory is writable by other users; refusing to run configurations from it")
		m.stopAll(stopDeadline)
		m.writeStatus()
		return
	}

	entries, err := os.ReadDir(m.configDir)
	if err != nil {
		if !os.IsNotExist(err) {
			logf("cannot read configuration directory: %v", err)
		}
		entries = nil
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".kbd") {
			continue
		}
		config := filepath.Join(m.configDir, entry.Name())
		activeConfigs[config] = true
		if unsafe, err := worldWritable(config); err != nil {
			logf("cannot inspect %s; stopping it: %v", entry.Name(), err)
			m.stopAndDelete(config, stopDeadline)
			continue
		} else if unsafe {
			logf("configuration %s is writable by other users; refusing to run it", entry.Name())
			m.stopAndDelete(config, stopDeadline)
			continue
		}

		device, signature, err := readConfig(config)
		if err != nil {
			if errors.Is(err, errConfigChanged) {
				logf("configuration %s changed while it was being read; retrying", entry.Name())
				continue
			}
			logf("cannot read %s; stopping it: %v", entry.Name(), err)
			m.stopAndDelete(config, stopDeadline)
			continue
		}
		if !deviceReady(device) {
			m.stopAndDelete(config, stopDeadline)
			continue
		}
		identity, err := deviceID(device)
		if err != nil {
			logf("%s vanished while checking its device; stopping it", entry.Name())
			m.stopAndDelete(config, stopDeadline)
			continue
		}
		if primary, ok := activeDevices[identity]; ok {
			if m.duplicates[config] != identity {
				logf("skipping %s; it duplicates %s", entry.Name(), filepath.Base(primary))
				m.duplicates[config] = identity
			}
			m.stopAndDelete(config, stopDeadline)
			continue
		}
		activeDevices[identity] = config
		delete(m.duplicates, config)

		state := m.states[config]
		if state == nil {
			state = &configState{}
			m.states[config] = state
		}
		if state.deviceID != identity || state.signature != signature {
			m.stopProcess(config, state, stopDeadline)
			state.failures = 0
			state.retryAfter = time.Time{}
			state.deviceID = identity
			state.signature = signature
		}

		if state.process != nil {
			select {
			case <-state.process.done:
				if state.process.exitErr != nil {
					logf("%s exited: %v", entry.Name(), state.process.exitErr)
				}
				state.process = nil
				m.scheduleRetry(config, state, now)
			default:
				if now.Sub(state.process.startedAt) >= 30*time.Second {
					state.failures = 0
					state.retryAfter = time.Time{}
				}
				continue
			}
		}
		if now.Before(state.retryAfter) {
			continue
		}
		m.startConfig(config, state, now)
	}

	for config := range m.states {
		if !activeConfigs[config] {
			m.stopAndDelete(config, stopDeadline)
		}
	}
	for config := range m.duplicates {
		if !activeConfigs[config] {
			delete(m.duplicates, config)
		}
	}
	m.writeStatus()
}

func (m *manager) stopAndDelete(config string, deadline time.Time) {
	if state := m.states[config]; state != nil {
		m.stopProcess(config, state, deadline)
		delete(m.states, config)
	}
}

func (m *manager) stopAll(deadline time.Time) {
	for config, state := range m.states {
		m.stopProcess(config, state, deadline)
		delete(m.states, config)
	}
}

func (m *manager) startConfig(config string, state *configState, now time.Time) {
	if err := m.dryRun(config); err != nil {
		logf("invalid configuration %s", filepath.Base(config))
		m.scheduleRetry(config, state, now)
		return
	}

	cmd := exec.Command(m.kmonadCommand, config)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid:   true,
		Pdeathsig: syscall.SIGTERM,
	}
	if err := cmd.Start(); err != nil {
		logf("failed to start %s: %v", filepath.Base(config), err)
		m.scheduleRetry(config, state, now)
		return
	}
	process := &processState{cmd: cmd, done: make(chan struct{}), startedAt: now}
	state.process = process
	logf("starting %s", filepath.Base(config))
	go func() {
		process.exitErr = cmd.Wait()
		close(process.done)
	}()
}

func (m *manager) dryRun(config string) error {
	cmd := exec.Command(m.kmonadCommand, "--dry-run", config)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (m *manager) scheduleRetry(config string, state *configState, now time.Time) {
	state.failures++
	delay := 60 * time.Second
	if state.failures < 6 {
		delay = time.Duration(1<<state.failures) * time.Second
	}
	state.retryAfter = now.Add(delay)
	logf("retrying %s in %s", filepath.Base(config), delay)
}

func (m *manager) stopProcess(config string, state *configState, deadline time.Time) {
	process := state.process
	if process == nil {
		return
	}
	logf("stopping %s", filepath.Base(config))
	signalProcess(process.cmd, syscall.SIGTERM)
	remaining := time.Until(deadline)
	if remaining < 0 {
		remaining = 0
	}
	select {
	case <-process.done:
		state.process = nil
		return
	case <-time.After(remaining):
	}
	if processStillRunning(process) {
		logf("force-stopping %s", filepath.Base(config))
		signalProcess(process.cmd, syscall.SIGKILL)
		select {
		case <-process.done:
		case <-time.After(time.Second):
		}
	}
	state.process = nil
}

func processStillRunning(process *processState) bool {
	select {
	case <-process.done:
		return false
	default:
		return true
	}
}

func signalProcess(cmd *exec.Cmd, signal syscall.Signal) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if err := syscall.Kill(-cmd.Process.Pid, signal); err != nil {
		_ = cmd.Process.Signal(signal)
	}
}

func (m *manager) cleanup() {
	deadline := time.Now().Add(m.stopTimeout)
	for config, state := range m.states {
		m.stopProcess(config, state, deadline)
	}
	if m.statusPath != "" {
		_ = os.Remove(m.statusPath)
	}
}

func (m *manager) writeStatus() {
	if m.statusPath == "" {
		return
	}
	entries, err := os.ReadDir(m.configDir)
	if err != nil {
		return
	}
	status := statusFile{PID: os.Getpid(), UpdatedAt: time.Now(), ConfigDir: m.configDir}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".kbd") {
			continue
		}
		config := filepath.Join(m.configDir, entry.Name())
		item := statusConfig{Name: entry.Name(), State: "waiting"}
		if device, readErr := readDeviceFile(config); readErr == nil {
			item.Device = device
		}
		if identity, err := deviceID(item.Device); err == nil {
			if _, duplicate := m.duplicates[config]; duplicate {
				item.State = "duplicate"
			} else if primary, exists := findPrimary(m, identity, config); exists && primary != config {
				item.State = "duplicate"
			}
		}
		if state := m.states[config]; state != nil {
			if state.process != nil {
				item.State = "running"
				item.ProcessID = state.process.cmd.Process.Pid
				item.ProcessStart = processStartTime(item.ProcessID)
			} else if time.Now().Before(state.retryAfter) {
				item.State = "backoff"
			} else if item.Device == "" || !deviceReady(item.Device) {
				item.State = "waiting"
			}
		}
		status.Configurations = append(status.Configurations, item)
	}
	sort.Slice(status.Configurations, func(i, j int) bool {
		return status.Configurations[i].Name < status.Configurations[j].Name
	})
	data, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return
	}
	tmp := m.statusPath + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return
	}
	if err := os.Rename(tmp, m.statusPath); err != nil {
		_ = os.Remove(tmp)
	}
}

func findPrimary(m *manager, identity, current string) (string, bool) {
	for config, state := range m.states {
		if config != current && state.deviceID == identity {
			return config, true
		}
	}
	return "", false
}

func readDeviceFile(config string) (string, error) {
	data, err := os.ReadFile(config)
	if err != nil {
		return "", err
	}
	return deviceFileFromData(data)
}

func readConfig(path string) (string, string, error) {
	before, err := fileSignature(path)
	if err != nil {
		return "", "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	after, err := fileSignature(path)
	if err != nil {
		return "", "", err
	}
	if before != after {
		return "", "", errConfigChanged
	}
	device, err := deviceFileFromData(data)
	return device, after, err
}

func deviceFileFromData(data []byte) (string, error) {
	tokens, err := tokenizeConfig(data)
	if err != nil {
		return "", err
	}
	for i := 0; i+3 < len(tokens); i++ {
		if tokens[i].kind == 's' && tokens[i].value == "input" &&
			tokens[i+1].kind == '(' &&
			tokens[i+2].kind == 's' && tokens[i+2].value == "device-file" &&
			tokens[i+3].kind == 'q' {
			return tokens[i+3].value, nil
		}
	}
	return "", nil
}

func tokenizeConfig(data []byte) ([]configToken, error) {
	tokens := make([]configToken, 0)
	for i := 0; i < len(data); {
		switch data[i] {
		case ';':
			for i < len(data) && data[i] != '\n' {
				i++
			}
		case ' ', '\t', '\r', '\n':
			i++
		case '(', ')':
			tokens = append(tokens, configToken{kind: data[i]})
			i++
		case '"':
			start := i
			closed := false
			i++
			for i < len(data) {
				if data[i] == '\\' {
					i += 2
					continue
				}
				if data[i] == '"' {
					i++
					value, err := strconv.Unquote(string(data[start:i]))
					if err != nil {
						return nil, fmt.Errorf("invalid quoted string: %w", err)
					}
					tokens = append(tokens, configToken{kind: 'q', value: value})
					closed = true
					break
				}
				i++
			}
			if !closed {
				return nil, fmt.Errorf("unterminated quoted string")
			}
		default:
			start := i
			for i < len(data) && !strings.ContainsRune("();\t\r\n ", rune(data[i])) {
				i++
			}
			tokens = append(tokens, configToken{kind: 's', value: string(data[start:i])})
		}
	}
	return tokens, nil
}

func deviceReady(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	return file.Close() == nil
}

func uinputReady(path string) bool {
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

func worldWritable(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	return info.Mode().Perm()&0o002 != 0, nil
}

func deviceID(path string) (string, error) {
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

func fileSignature(path string) (string, error) {
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

func logf(format string, args ...any) {
	message := fmt.Sprintf(format, args...)
	if os.Getenv("KMONAD_LOG_FORMAT") == "json" {
		data, _ := json.Marshal(map[string]any{
			"message": message,
			"time":    time.Now().UTC(),
		})
		fmt.Fprintf(os.Stderr, "%s\n", data)
		return
	}
	fmt.Fprintf(os.Stderr, "kmonad-device-manager: %s\n", message)
}

type doctorOutput struct {
	failures int
	green    string
	red      string
	yellow   string
	reset    string
}

func newDoctorOutput() *doctorOutput {
	d := &doctorOutput{}
	if os.Getenv("KMONAD_DOCTOR_COLOR") == "always" || (os.Getenv("KMONAD_DOCTOR_COLOR") != "never" && isTerminal(os.Stdout)) {
		d.green, d.red, d.yellow, d.reset = "\033[32m", "\033[31m", "\033[33m", "\033[0m"
	}
	return d
}

func isTerminal(file *os.File) bool {
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func (d *doctorOutput) ok(message string) {
	fmt.Fprintf(os.Stdout, "%s[ok]%s %s\n", d.green, d.reset, message)
}
func (d *doctorOutput) wait(message string) {
	fmt.Fprintf(os.Stdout, "%s[wait]%s %s\n", d.yellow, d.reset, message)
}
func (d *doctorOutput) bad(message string) {
	d.failures++
	fmt.Fprintf(os.Stdout, "%s[bad]%s %s\n", d.red, d.reset, message)
}

func doctor(s settings) int {
	d := newDoctorOutput()
	fmt.Fprintln(os.Stdout, "KMonad Device Manager doctor")
	fmt.Fprintf(os.Stdout, "Configuration directory: %s\n", s.configDir)

	if path, err := exec.LookPath(s.kmonadCommand); err == nil {
		d.ok("KMonad: " + path)
	} else {
		d.bad("KMonad: not found on PATH")
	}
	if s.pollInterval == 0 {
		d.bad(fmt.Sprintf("Poll interval: '%s' is invalid", s.pollIntervalRaw))
	} else {
		d.ok(fmt.Sprintf("Poll interval: %ss", s.pollIntervalRaw))
	}
	if s.stopTimeout == 0 {
		d.bad(fmt.Sprintf("Stop timeout: '%s' is invalid", s.stopTimeoutRaw))
	} else {
		d.ok(fmt.Sprintf("Stop timeout: %ss", s.stopTimeoutRaw))
	}
	if inGroup("input") {
		d.ok("Group membership: input")
	} else {
		d.bad("Group membership: input is missing")
	}
	if inGroup("uinput") {
		d.ok("Group membership: uinput")
	} else {
		d.bad("Group membership: uinput is missing")
	}
	if _, err := os.Stat("/sys/module/uinput"); err == nil {
		d.ok("Kernel module: uinput is loaded")
	} else {
		d.bad("Kernel module: uinput is not loaded")
	}
	if uinputReady("/dev/uinput") {
		d.ok("Device access: /dev/uinput is readable and writable")
	} else {
		d.bad("Device access: /dev/uinput is unavailable or inaccessible")
	}

	entries, err := os.ReadDir(s.configDir)
	if err != nil {
		d.bad("Configuration directory does not exist")
	} else {
		if unsafe, securityErr := worldWritable(s.configDir); securityErr == nil && unsafe {
			d.bad("Configuration directory is writable by other users")
		} else {
			d.ok("Configuration directory exists")
		}
		configs := make([]string, 0)
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".kbd") {
				configs = append(configs, filepath.Join(s.configDir, entry.Name()))
			}
		}
		sort.Strings(configs)
		if len(configs) == 0 {
			d.bad("Configuration files: no .kbd files found")
		}
		for _, config := range configs {
			device, readErr := readDeviceFile(config)
			name := filepath.Base(config)
			if unsafe, securityErr := worldWritable(config); securityErr == nil && unsafe {
				d.bad(fmt.Sprintf("Configuration %s: writable by other users", name))
			}
			if readErr != nil {
				d.bad(fmt.Sprintf("Configuration %s: cannot read configuration", name))
				continue
			}
			if device == "" {
				d.bad(fmt.Sprintf("Configuration %s: no device-file input", name))
			} else if _, statErr := os.Stat(device); os.IsNotExist(statErr) {
				d.wait(fmt.Sprintf("Input %s: %s is not connected", name, device))
			} else if !deviceReady(device) {
				d.bad(fmt.Sprintf("Input %s: %s is inaccessible or not a character device", name, device))
			} else {
				d.ok(fmt.Sprintf("Input %s: %s", name, device))
			}
			if _, err := exec.LookPath(s.kmonadCommand); err == nil {
				cmd := exec.Command(s.kmonadCommand, "--dry-run", config)
				cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
				if err := cmd.Run(); err == nil {
					d.ok(fmt.Sprintf("Configuration %s: parses successfully", name))
				} else {
					d.bad(fmt.Sprintf("Configuration %s: KMonad validation failed", name))
				}
			}
		}
	}

	if _, err := exec.LookPath("systemctl"); err != nil {
		d.bad("Service check: systemctl is missing")
	} else {
		if exec.Command("systemctl", "--user", "is-enabled", "kmonad-device-manager.service").Run() == nil {
			d.ok("Service: enabled")
		} else {
			d.bad("Service: not enabled")
		}
		if exec.Command("systemctl", "--user", "is-active", "kmonad-device-manager.service").Run() == nil {
			d.ok("Service: active")
		} else {
			d.bad("Service: inactive")
		}
	}
	if d.failures > 255 {
		return 255
	}
	return d.failures
}

func showStatus() int {
	base, err := runtimeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "kmonad-device-manager: cannot locate runtime directory: %v\n", err)
		return 1
	}
	data, err := os.ReadFile(filepath.Join(base, "status.json"))
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintln(os.Stderr, "kmonad-device-manager: manager is not running")
			return 3
		}
		fmt.Fprintf(os.Stderr, "kmonad-device-manager: cannot read status: %v\n", err)
		return 1
	}
	var status statusFile
	if err := json.Unmarshal(data, &status); err != nil {
		fmt.Fprintf(os.Stderr, "kmonad-device-manager: invalid status file: %v\n", err)
		return 1
	}
	if !processExists(status.PID) {
		fmt.Fprintln(os.Stderr, "kmonad-device-manager: manager is not running")
		return 3
	}
	fmt.Printf("Manager PID: %d\n", status.PID)
	fmt.Printf("Configuration directory: %s\n", status.ConfigDir)
	fmt.Printf("Updated: %s\n", status.UpdatedAt.Format(time.RFC3339))
	for _, config := range status.Configurations {
		if config.ProcessID != 0 {
			fmt.Printf("%s: %s (PID %d)\n", config.Name, config.State, config.ProcessID)
		} else {
			fmt.Printf("%s: %s\n", config.Name, config.State)
		}
	}
	return 0
}

func processExists(pid int) bool {
	if pid <= 0 {
		return false
	}
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	return err == nil && strings.Contains(string(data), "kmonad-device-manager")
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
		signalProcessID(config.ProcessID, syscall.SIGTERM)
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) && pidExists(config.ProcessID) {
			time.Sleep(25 * time.Millisecond)
		}
		if pidExists(config.ProcessID) {
			signalProcessID(config.ProcessID, syscall.SIGKILL)
		}
	}
	_ = os.Remove(statusPath)
}

func signalProcessID(pid int, signal syscall.Signal) {
	if err := syscall.Kill(-pid, signal); err != nil {
		_ = syscall.Kill(pid, signal)
	}
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
