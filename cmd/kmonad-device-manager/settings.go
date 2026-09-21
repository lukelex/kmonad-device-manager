package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

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
		configDir:          configDir,
		kmonadCommand:      valueOr("KMONAD_COMMAND", "kmonad"),
		pollIntervalRaw:    valueOr("KMONAD_POLL_INTERVAL", "2"),
		stopTimeoutRaw:     valueOr("KMONAD_STOP_TIMEOUT", "5"),
		dryRunTimeoutRaw:   valueOr("KMONAD_DRY_RUN_TIMEOUT", "30"),
		maxConfigsRaw:      valueOr("KMONAD_MAX_CONFIGS", "128"),
		watchdogTimeoutRaw: valueOr("KMONAD_WATCHDOG_TIMEOUT", "60"),
		metricsAddr:        os.Getenv("KMONAD_METRICS_ADDR"),
		cgroupRoot:         os.Getenv("KMONAD_CGROUP_ROOT"),
		processMemoryMax:   os.Getenv("KMONAD_PROCESS_MEMORY_MAX"),
		processCPUQuota:    os.Getenv("KMONAD_PROCESS_CPU_MAX"),
	}
	s.pollInterval = seconds(s.pollIntervalRaw)
	s.stopTimeout = seconds(s.stopTimeoutRaw)
	s.dryRunTimeout = seconds(s.dryRunTimeoutRaw)
	s.watchdogTimeout = seconds(s.watchdogTimeoutRaw)
	s.maxConfigs = positiveInteger(s.maxConfigsRaw)
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

func positiveInteger(value string) int {
	n, err := strconv.Atoi(value)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

func validateSettings(s settings) error {
	if s.pollInterval == 0 {
		return fmt.Errorf("KMONAD_POLL_INTERVAL must be a positive integer")
	}
	if s.stopTimeout == 0 {
		return fmt.Errorf("KMONAD_STOP_TIMEOUT must be a positive integer")
	}
	if s.dryRunTimeout == 0 {
		return fmt.Errorf("KMONAD_DRY_RUN_TIMEOUT must be a positive integer")
	}
	if s.watchdogTimeout == 0 {
		return fmt.Errorf("KMONAD_WATCHDOG_TIMEOUT must be a positive integer")
	}
	if s.maxConfigs == 0 {
		return fmt.Errorf("KMONAD_MAX_CONFIGS must be a positive integer")
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
