package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

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
		item := statusConfig{Name: entry.Name(), State: "waiting", Healthy: false, Reason: "configuration not loaded"}
		if device, readErr := readDeviceFile(config); readErr == nil {
			item.Device = device
			item.Connected = deviceReady(device)
			if !item.Connected {
				item.Reason = "device unavailable"
			} else {
				item.Reason = "configuration not loaded"
			}
		}
		if identity, err := deviceID(item.Device); err == nil {
			if _, duplicate := m.duplicates[config]; duplicate {
				item.State = "duplicate"
				item.Reason = "duplicate device claim"
			} else if primary, exists := findPrimary(m, identity, config); exists && primary != config {
				item.State = "duplicate"
			}
		}
		if state := m.states[config]; state != nil {
			item.Failures = state.failures
			item.RetryAfter = state.retryAfter
			if state.phase != "" {
				item.State = string(state.phase)
			}
			if state.process != nil {
				item.State = "running"
				item.Healthy = m.processHealthy(config, state.process)
				item.Reason = "process healthy"
				if !item.Healthy {
					item.Reason = "process ownership or health check failed"
				}
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

func readStatusFile(path string) *statusFile {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var status statusFile
	if json.Unmarshal(data, &status) != nil {
		return nil
	}
	return &status
}

func (m *manager) restoreBackoff(status *statusFile) {
	if status == nil {
		return
	}
	for _, item := range status.Configurations {
		if item.Failures == 0 && item.RetryAfter.IsZero() {
			continue
		}
		config := filepath.Join(m.configDir, item.Name)
		m.states[config] = &configState{
			phase:      phaseFailed,
			failures:   item.Failures,
			retryAfter: item.RetryAfter,
		}
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
