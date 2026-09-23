package manager

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// externalConfiguration is private sidecar state. Its path and signature never
// cross the API boundary; clients receive only the corresponding Configuration.
type externalConfiguration struct {
	ID        string                 `json:"id"`
	Ownership ConfigurationOwnership `json:"ownership"`
	Path      string                 `json:"path"`
	Name      string                 `json:"name"`
	DeviceID  string                 `json:"device_id"`
	Signature string                 `json:"signature"`
}

type externalConfigurationRegistryFile struct {
	Configurations []externalConfiguration `json:"configurations"`
}

func (m *manager) refreshExternalConfigurationRegistry() {
	if m.configDir == "" {
		return
	}
	entries, err := os.ReadDir(m.configDir)
	if err != nil {
		return
	}
	configurations := make(map[string]externalConfiguration)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".kbd") {
			continue
		}
		path := filepath.Join(m.configDir, entry.Name())
		data, err := readFileLimited(path, m.configurationByteLimit())
		if err != nil {
			continue
		}
		configuration := externalConfiguration{
			ID: opaqueExternalConfigurationID(path), Ownership: ConfigurationExternal,
			Path: path, Name: entry.Name(), Signature: configurationDigest(data),
			DeviceID: m.deviceIDForExternalConfiguration(data),
		}
		configurations[configuration.ID] = configuration
	}
	m.externalConfigs = configurations
	m.writeExternalConfigurationRegistry()
}

func (m *manager) loadExternalConfigurationRegistry() {
	if m.externalRegistryPath == "" {
		return
	}
	data, err := os.ReadFile(m.externalRegistryPath)
	if err != nil {
		return
	}
	var registry externalConfigurationRegistryFile
	if json.Unmarshal(data, &registry) != nil {
		return
	}
	if m.externalConfigs == nil {
		m.externalConfigs = make(map[string]externalConfiguration)
	}
	for _, configuration := range registry.Configurations {
		if configuration.ID != "" && configuration.Ownership == ConfigurationExternal {
			m.externalConfigs[configuration.ID] = configuration
		}
	}
}

func (m *manager) writeExternalConfigurationRegistry() {
	if m.externalRegistryPath == "" {
		return
	}
	configurations := make([]externalConfiguration, 0, len(m.externalConfigs))
	for _, configuration := range m.externalConfigs {
		configurations = append(configurations, configuration)
	}
	sort.Slice(configurations, func(i, j int) bool { return configurations[i].ID < configurations[j].ID })
	data, err := json.Marshal(externalConfigurationRegistryFile{Configurations: configurations})
	if err == nil {
		_ = writeAtomicPrivateFile(m.externalRegistryPath, append(data, '\n'), 0o600)
	}
}

func (m *manager) managedConfigurationIntact(configuration managedConfiguration) bool {
	data, err := readFileLimited(m.managedConfigurationPath(configuration), m.configurationByteLimit())
	intact := err == nil && configurationDigest(data) == configuration.Digest
	if m.managedTampered == nil {
		m.managedTampered = make(map[string]bool)
	}
	m.managedTampered[configuration.ID] = !intact
	return intact
}

func (m *manager) configurationByteLimit() int64 {
	if m.maxConfigBytes > 0 {
		return m.maxConfigBytes
	}
	return defaultMaxConfigBytes
}

func opaqueExternalConfigurationID(path string) string {
	digest := sha256.Sum256([]byte(path))
	return "cfg_" + hex.EncodeToString(digest[:16])
}

func (m *manager) deviceIDForExternalConfiguration(content []byte) string {
	device, err := deviceFileFromData(content)
	if err != nil || device == "" {
		return ""
	}
	identity, err := deviceID(device)
	if err != nil {
		return ""
	}
	discovered, err := discoverKeyboardDevices()
	if err != nil {
		return ""
	}
	for _, candidate := range discovered {
		candidateIdentity, err := deviceID(candidate.nodePath)
		if err == nil && candidateIdentity == identity {
			return candidate.device.ID
		}
	}
	return ""
}

func (m *manager) configurationList() []Configuration {
	m.refreshExternalConfigurationRegistry()
	configurations := make([]Configuration, 0, len(m.managedConfigs)+len(m.externalConfigs))
	for _, managed := range m.managedConfigs {
		configurations = append(configurations, m.managedConfigurationResource(managed))
	}
	for _, external := range m.externalConfigs {
		configurations = append(configurations, m.externalConfigurationResource(external))
	}
	sort.Slice(configurations, func(i, j int) bool { return configurations[i].ID < configurations[j].ID })
	return configurations
}

func (m *manager) managedConfigurationResource(configuration managedConfiguration) Configuration {
	runtime := m.runtimeForConfiguration(m.managedConfigurationPath(configuration), configuration.Model.DeviceID, configuration.Enabled)
	if !m.managedConfigurationIntact(configuration) {
		runtime = RuntimeState{Phase: RuntimeFailed, ReasonCode: ReasonConfigurationChanged, Reason: "manager-owned revision was changed outside the manager"}
	}
	activeRevision := uint64(0)
	if runtime.Phase == RuntimeRunning {
		activeRevision = configuration.ContentRevision
	}
	return Configuration{ID: configuration.ID, Name: configuration.Name, Ownership: ConfigurationManaged,
		Enabled: configuration.Enabled, DeviceID: configuration.Model.DeviceID, DesiredRevision: configuration.Revision,
		ActiveRevision: activeRevision, Runtime: runtime}
}

func (m *manager) externalConfigurationResource(configuration externalConfiguration) Configuration {
	runtime := m.runtimeForConfiguration(configuration.Path, configuration.DeviceID, true)
	return Configuration{ID: configuration.ID, Name: configuration.Name, Ownership: ConfigurationExternal,
		Enabled: true, DeviceID: configuration.DeviceID, Runtime: runtime}
}

func (m *manager) runtimeForConfiguration(path, deviceID string, enabled bool) RuntimeState {
	if !enabled {
		return RuntimeState{Phase: RuntimeDisabled, ReasonCode: ReasonConfigurationDisabled, Reason: "configuration is disabled"}
	}
	if state := m.states[path]; state != nil {
		if state.process != nil && m.processHealthy(path, state.process) {
			return RuntimeState{Phase: RuntimeRunning, ReasonCode: ReasonRuntimeRunning, Reason: "process healthy", Connected: true, Healthy: true, FailureCount: state.failures}
		}
		phase := runtimePhase(state.phase)
		code, reason := ReasonRuntimeWaitingForDevice, "configuration is waiting for its keyboard"
		if phase == RuntimeFailed {
			code, reason = ReasonRuntimeActivationFailed, "configuration is waiting to retry after a failed activation"
		} else if phase == RuntimeDuplicate {
			code, reason = ReasonRuntimeDuplicateDevice, "another configuration currently claims this keyboard"
		} else if phase == RuntimeStopped {
			code, reason = ReasonRuntimeStopped, "configuration process is stopped"
		}
		return RuntimeState{Phase: phase, ReasonCode: code, Reason: reason, FailureCount: state.failures}
	}
	if device, exists := m.devices[deviceID]; exists {
		return RuntimeState{Phase: RuntimeWaiting, ReasonCode: device.ReasonCode, Reason: device.Reason, Connected: device.Availability == DeviceConnected}
	}
	return RuntimeState{Phase: RuntimeDiscovered, ReasonCode: ReasonConfigurationDiscovered, Reason: "configuration is awaiting reconciliation"}
}

func runtimePhase(phase configPhase) RuntimePhase {
	switch phase {
	case phaseValidating:
		return RuntimeValidating
	case phaseWaiting:
		return RuntimeWaiting
	case phaseRunning:
		return RuntimeRunning
	case phaseFailed:
		return RuntimeFailed
	case phaseDuplicate:
		return RuntimeDuplicate
	case phaseStopped:
		return RuntimeStopped
	default:
		return RuntimeDiscovered
	}
}
