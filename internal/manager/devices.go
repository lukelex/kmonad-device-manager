package manager

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"text/tabwriter"

	"github.com/lukelex/kmonad-device-manager/internal/platform"
)

type deviceRegistryFile struct {
	Devices []Device `json:"devices"`
}

var listKeyboards = func() ([]platform.KeyboardDevice, error) { return host.ListKeyboards() }

type discoveredKeyboard struct {
	device   Device
	nodePath string
	virtual  bool
}

func (m *manager) loadDeviceRegistry() {
	if m.deviceRegistryPath == "" {
		return
	}
	data, err := os.ReadFile(m.deviceRegistryPath)
	if err != nil {
		return
	}
	var registry deviceRegistryFile
	if json.Unmarshal(data, &registry) != nil {
		return
	}
	for _, device := range registry.Devices {
		if device.ID != "" {
			// Device registries written before roles existed represent only
			// physical/configurable devices. A later refresh upgrades any
			// retained manager output whose known generated name matches.
			if device.Role == "" {
				device.Role = DeviceRoleInput
			}
			m.devices[device.ID] = device
		}
	}
}

func (m *manager) refreshDevices() {
	if m.devices == nil {
		m.devices = make(map[string]Device)
	}
	current, err := discoverKeyboardDevices()
	if err != nil {
		logf("cannot enumerate keyboards: %v", err)
		return
	}
	for id, device := range m.devices {
		device.Availability, device.ReasonCode, device.Reason = DeviceDisconnected, ReasonDeviceDisconnected, "keyboard is disconnected"
		device.RuntimeConflict = false
		m.devices[id] = device
	}
	claims := m.deviceClaims()
	for _, discovered := range current {
		device := discovered.device
		device.Role = m.classifyDeviceRole(device, discovered.virtual)
		if nodeID, err := deviceID(discovered.nodePath); err == nil {
			device.ConfiguredBy = claims[nodeID]
		}
		if len(device.ConfiguredBy) > 1 {
			device.Availability = DeviceConflicting
			device.RuntimeConflict = true
			device.ReasonCode = ReasonDeviceConflicting
			device.Reason = "keyboard is claimed by multiple configurations"
		}
		m.devices[device.ID] = device
	}
	m.reclassifyRetainedManagerOutputs(current)
	m.writeDeviceRegistry()
}

// classifyDeviceRole requires both a name generated for one of this manager's
// configurations and virtual-device metadata. The name check alone must never
// hide a physical keyboard which happens to have a similar display name.
func (m *manager) classifyDeviceRole(device Device, virtual bool) DeviceRole {
	if previous, known := m.devices[device.ID]; known && previous.Role == DeviceRoleManagerOutput && virtual {
		return DeviceRoleManagerOutput
	}
	if virtual && m.isManagedOutputName(device.DisplayName) {
		return DeviceRoleManagerOutput
	}
	return DeviceRoleInput
}

func (m *manager) isManagedOutputName(name string) bool {
	for _, configuration := range m.managedConfigs {
		if managedOutputName(configuration.Model.DeviceID) == name {
			return true
		}
	}
	return false
}

// reclassifyRetainedManagerOutputs migrates registries from before Device.Role.
// It applies only to records absent from current discovery: a currently
// discovered non-virtual physical keyboard always remains an input even when
// its display name matches a manager output name.
func (m *manager) reclassifyRetainedManagerOutputs(current []discoveredKeyboard) {
	connected := make(map[string]struct{}, len(current))
	for _, discovered := range current {
		connected[discovered.device.ID] = struct{}{}
	}
	for id, device := range m.devices {
		if _, found := connected[id]; found || device.Role == DeviceRoleManagerOutput || !m.isManagedOutputName(device.DisplayName) {
			continue
		}
		device.Role = DeviceRoleManagerOutput
		m.devices[id] = device
	}
}

func (m *manager) managerOutputForNodePath(path string) (Device, bool) {
	targetID, err := deviceID(path)
	if err != nil {
		return Device{}, false
	}
	discovered, err := discoverKeyboardDevices()
	if err != nil {
		return Device{}, false
	}
	for _, keyboard := range discovered {
		nodeID, err := deviceID(keyboard.nodePath)
		if err != nil || nodeID != targetID {
			continue
		}
		device, known := m.devices[keyboard.device.ID]
		return device, known && device.Role == DeviceRoleManagerOutput
	}
	return Device{}, false
}

func (m *manager) deviceClaims() map[string][]string {
	claims := make(map[string][]string)
	paths, err := m.configurationPaths()
	if err != nil {
		return claims
	}
	for _, path := range paths {
		device, err := readDeviceFileWithLimit(path, m.maxConfigBytes)
		if err != nil || device == "" {
			continue
		}
		id, err := deviceID(device)
		if err == nil {
			claims[id] = append(claims[id], m.configurationClaimName(path))
		}
	}
	for id := range claims {
		sort.Strings(claims[id])
	}
	return claims
}

func (m *manager) configurationClaimName(path string) string {
	for id, configuration := range m.managedConfigs {
		if m.managedConfigurationPath(configuration) == path {
			return id
		}
	}
	return filepath.Base(path)
}

func (m *manager) writeDeviceRegistry() {
	if m.deviceRegistryPath == "" {
		return
	}
	devices := m.deviceList()
	data, err := json.Marshal(deviceRegistryFile{Devices: devices})
	if err != nil {
		return
	}
	tmp := m.deviceRegistryPath + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return
	}
	if err := os.Rename(tmp, m.deviceRegistryPath); err != nil {
		_ = os.Remove(tmp)
	}
}

func (m *manager) deviceList() []Device {
	devices := make([]Device, 0, len(m.devices))
	for _, device := range m.devices {
		devices = append(devices, device)
	}
	sort.Slice(devices, func(i, j int) bool { return devices[i].ID < devices[j].ID })
	return devices
}

func discoverDevices() ([]Device, error) {
	discovered, err := discoverKeyboardDevices()
	if err != nil {
		return nil, err
	}
	devices := make([]Device, 0, len(discovered))
	for _, device := range discovered {
		devices = append(devices, device.device)
	}
	return devices, nil
}

func discoverKeyboardDevices() ([]discoveredKeyboard, error) {
	found, err := listKeyboards()
	if err != nil {
		return nil, err
	}
	identities := make(map[string]int, len(found))
	for _, device := range found {
		identities[device.Identity]++
	}
	devices := make([]discoveredKeyboard, 0, len(found))
	for _, device := range found {
		stability := IdentityUnknown
		if device.IdentityStability == "serial" {
			stability = IdentitySerial
		} else if device.IdentityStability == "topology" {
			stability = IdentityTopology
		}
		identity := device.Identity
		if identities[identity] > 1 && device.FallbackIdentity != "" {
			identity, stability = device.FallbackIdentity, IdentityTopology
		}
		availability, reasonCode, reason := keyboardAvailability(device.Availability)
		devices = append(devices, discoveredKeyboard{nodePath: device.NodePath, device: Device{
			ID: opaqueDeviceID(identity), DisplayName: device.DisplayName,
			Vendor: device.Vendor, Product: device.Product, Serial: device.Serial,
			Role: DeviceRoleInput, Availability: availability, IdentityStability: stability,
			ConfiguredBy: []string{}, ReasonCode: reasonCode, Reason: reason,
		}, virtual: device.Virtual})
	}
	sort.Slice(devices, func(i, j int) bool { return devices[i].device.ID < devices[j].device.ID })
	return devices, nil
}

func keyboardAvailability(availability platform.DeviceAvailability) (DeviceAvailability, ReasonCode, string) {
	switch availability {
	case platform.DeviceInaccessible:
		return DeviceInaccessible, ReasonDeviceInaccessible, "keyboard is connected but inaccessible"
	case platform.DeviceUnsupported:
		return DeviceUnsupported, ReasonDeviceUnsupported, "keyboard input interface is unsupported"
	case platform.DeviceDisconnected:
		return DeviceDisconnected, ReasonDeviceDisconnected, "keyboard is disconnected"
	default:
		return DeviceConnected, ReasonDeviceConnected, "keyboard is connected and accessible"
	}
}

func opaqueDeviceID(identity string) string {
	digest := sha256.Sum256([]byte(identity))
	return "dev_" + hex.EncodeToString(digest[:16])
}

func showDevices(jsonOutput bool) int {
	base, err := runtimeDir()
	if err != nil {
		writeCLIError(os.Stderr, jsonOutput, "runtime_directory_unavailable", err.Error())
		return 1
	}
	settings := loadSettings()
	m := &manager{
		configDir: settings.configDir, maxConfigBytes: settings.maxConfigBytes,
		devices: make(map[string]Device), deviceRegistryPath: filepath.Join(base, "devices.json"),
	}
	m.loadDeviceRegistry()
	m.refreshDevices()
	devices := m.deviceList()
	if jsonOutput {
		if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"devices": devices}); err != nil {
			writeCLIError(os.Stderr, true, "output_failed", err.Error())
			return 1
		}
		return 0
	}
	writer := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "ID\tNAME\tROLE\tVENDOR\tPRODUCT\tSERIAL\tAVAILABILITY\tREASON CODE\tIDENTITY")
	for _, device := range devices {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", device.ID, device.DisplayName, device.Role, device.Vendor, device.Product, device.Serial, device.Availability, device.ReasonCode, device.IdentityStability)
	}
	_ = writer.Flush()
	return 0
}
