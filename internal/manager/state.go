package manager

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lukelex/kmonad-device-manager/internal/platform"
)

func (m *manager) writeStatus() {
	if m.statusPath == "" {
		return
	}
	entries, err := os.ReadDir(m.configDir)
	if err != nil && !os.IsNotExist(err) {
		m.statusWriteFailed(err)
		return
	}
	pid := os.Getpid()
	status := statusFile{PID: pid, ProcessStart: processStartTime(pid), UpdatedAt: time.Now(), ConfigDir: m.configDir}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".kbd") {
			continue
		}
		config := filepath.Join(m.configDir, entry.Name())
		item := statusConfig{Name: entry.Name(), State: "waiting", Healthy: false, ReasonCode: ReasonConfigurationDiscovered, Reason: "configuration not loaded"}
		if device, readErr := readDeviceFileWithLimit(config, m.maxConfigBytes); readErr == nil {
			item.Device = device
			item.Availability, item.AvailabilityReasonCode, item.Reason = configuredDeviceAvailability(device)
			item.Connected = item.Availability == DeviceConnected
			if !item.Connected {
				item.ReasonCode = item.AvailabilityReasonCode
			} else {
				item.Reason = "configuration not loaded"
			}
		}
		if identity, err := deviceID(item.Device); err == nil {
			if _, duplicate := m.duplicates[config]; duplicate {
				item.State = "duplicate"
				item.Availability = DeviceConflicting
				item.AvailabilityReasonCode = ReasonDeviceConflicting
				item.ReasonCode = ReasonDeviceConflicting
				item.Reason = "duplicate device claim"
			} else if primary, exists := findPrimary(m, identity, config); exists && primary != config {
				item.State = "duplicate"
				item.Availability = DeviceConflicting
				item.AvailabilityReasonCode = ReasonDeviceConflicting
				item.ReasonCode = ReasonDeviceConflicting
				item.Reason = "duplicate device claim"
			}
		}
		if state := m.states[config]; state != nil {
			item.Failures = state.failures
			item.RetryAfter = state.retryAfter
			item.FailureReason = state.failureReason
			item.LastKnownGoodSignature = state.lastKnownGoodSignature
			if state.phase != "" {
				item.State = string(state.phase)
			}
			if state.process != nil {
				item.State = "running"
				item.Healthy = m.processHealthy(config, state.process)
				item.ReasonCode = ReasonRuntimeRunning
				item.Reason = "process healthy"
				if !item.Healthy {
					item.ReasonCode = ReasonRuntimeProcessUnhealthy
					item.Reason = "process ownership or health check failed"
				}
				item.ProcessID = state.process.cmd.Process.Pid
				item.ProcessStart = processStartTime(item.ProcessID)
				item.ProcessGroupID = state.process.processGroupID
				item.LaunchPath = state.process.launchPath
			} else if time.Now().Before(state.retryAfter) {
				item.State = "backoff"
			} else if item.Device == "" || item.Availability != DeviceConnected {
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
		m.statusWriteFailed(err)
		return
	}
	tmp := m.statusPath + ".tmp"
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		m.statusWriteFailed(err)
		return
	}
	removeTemp := true
	defer func() {
		_ = file.Close()
		if removeTemp {
			_ = os.Remove(tmp)
		}
	}()
	if _, err := file.Write(append(data, '\n')); err != nil {
		m.statusWriteFailed(err)
		return
	}
	if err := file.Sync(); err != nil {
		m.statusWriteFailed(err)
		return
	}
	if err := file.Close(); err != nil {
		m.statusWriteFailed(err)
		return
	}
	if err := os.Rename(tmp, m.statusPath); err != nil {
		m.statusWriteFailed(err)
		return
	}
	removeTemp = false
	if err := syncDirectory(filepath.Dir(m.statusPath)); err != nil {
		m.statusWriteFailed(err)
	}
}

func (m *manager) statusWriteFailed(err error) {
	m.statusFailures.Add(1)
	logf("cannot persist manager status: %v", err)
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func readStatusFile(path string) *statusFile {
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			logf("cannot read manager status: %v", err)
		}
		return nil
	}
	var status statusFile
	if err := json.Unmarshal(data, &status); err != nil {
		logf("cannot decode manager status: %v", err)
		return nil
	}
	return &status
}

func (m *manager) restoreBackoff(status *statusFile) {
	if status == nil {
		return
	}
	for _, item := range status.Configurations {
		if item.Failures == 0 && item.RetryAfter.IsZero() && item.FailureReason == "" && item.LastKnownGoodSignature == "" {
			continue
		}
		config := filepath.Join(m.configDir, item.Name)
		m.states[config] = &configState{
			phase:                  phaseFailed,
			failures:               item.Failures,
			retryAfter:             item.RetryAfter,
			failureReason:          item.FailureReason,
			lastKnownGoodSignature: item.LastKnownGoodSignature,
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
	return readDeviceFileWithLimit(config, defaultMaxConfigBytes)
}

func readDeviceFileWithLimit(config string, maxBytes int64) (string, error) {
	data, err := readFileLimited(config, maxBytes)
	if err != nil {
		return "", err
	}
	return deviceFileFromData(data)
}

func readConfig(path string) (string, string, error) {
	return readConfigWithLimit(path, defaultMaxConfigBytes)
}

func readConfigWithLimit(path string, maxBytes int64) (string, string, error) {
	device, signature, _, err := readConfigDataWithLimit(path, maxBytes)
	return device, signature, err
}

func readConfigDataWithLimit(path string, maxBytes int64) (string, string, []byte, error) {
	before, err := fileSignature(path)
	if err != nil {
		return "", "", nil, err
	}
	data, err := readFileLimited(path, maxBytes)
	if err != nil {
		return "", "", nil, err
	}
	after, err := fileSignature(path)
	if err != nil {
		return "", "", nil, err
	}
	if before != after {
		return "", "", nil, errConfigChanged
	}
	device, err := deviceFileFromData(data)
	if err != nil {
		return "", "", nil, err
	}
	digest := sha256.Sum256(data)
	return device, hex.EncodeToString(digest[:]), data, nil
}

const defaultMaxConfigBytes int64 = 1 << 20

const configSnapshotPrefix = ".kmonad-device-manager-snapshot-"

func createConfigSnapshot(config string, data []byte) (string, error) {
	return createSnapshot(filepath.Dir(config), configSnapshotPrefix+filepath.Base(config)+"-", data)
}

func createValidationSnapshot(data []byte) (string, error) {
	base, err := runtimeDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", err
	}
	return createSnapshot(base, configSnapshotPrefix+"candidate-", data)
}

func createSnapshot(directory, pattern string, data []byte) (string, error) {
	file, err := os.CreateTemp(directory, pattern)
	if err != nil {
		return "", err
	}
	path := file.Name()
	remove := true
	defer func() {
		_ = file.Close()
		if remove {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(data); err != nil {
		return "", err
	}
	if err := file.Sync(); err != nil {
		return "", err
	}
	if err := file.Chmod(0o400); err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	remove = false
	return path, nil
}

func removeConfigSnapshot(path string) error {
	if path == "" || !strings.HasPrefix(filepath.Base(path), configSnapshotPrefix) {
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func readFileLimited(path string, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		return nil, fmt.Errorf("configuration size limit must be positive")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if info, err := file.Stat(); err == nil && info.Size() > maxBytes {
		return nil, fmt.Errorf("configuration exceeds %d-byte limit", maxBytes)
	}
	limit := maxBytes
	maxInt64 := int64(^uint64(0) >> 1)
	if limit < maxInt64 {
		limit++
	}
	data, err := io.ReadAll(io.LimitReader(file, limit))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("configuration exceeds %d-byte limit", maxBytes)
	}
	return data, nil
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
	availability, _, _ := configuredDeviceAvailability(path)
	return availability == DeviceConnected
}

func configuredDeviceAvailability(path string) (DeviceAvailability, ReasonCode, string) {
	switch host.DeviceAvailability(path) {
	case platform.DeviceInaccessible:
		return DeviceInaccessible, ReasonDeviceInaccessible, "input device is inaccessible"
	case platform.DeviceUnsupported:
		return DeviceUnsupported, ReasonDeviceUnsupported, "input path is not a supported character device"
	case platform.DeviceDisconnected:
		return DeviceDisconnected, ReasonDeviceDisconnected, "input device is disconnected"
	default:
		return DeviceConnected, ReasonDeviceConnected, "input device is connected and accessible"
	}
}

func uinputReady(path string) bool {
	return host.UinputReady(path)
}

func worldWritable(path string) (bool, error) {
	return host.WorldWritable(path)
}

func deviceID(path string) (string, error) {
	return host.DeviceID(path)
}

func fileSignature(path string) (string, error) {
	return host.FileSignature(path)
}
