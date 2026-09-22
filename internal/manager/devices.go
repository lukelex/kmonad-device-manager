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
			m.devices[device.ID] = device
		}
	}
}

func (m *manager) refreshDevices() {
	if m.devices == nil {
		m.devices = make(map[string]Device)
	}
	current, err := discoverDevices()
	if err != nil {
		logf("cannot enumerate keyboards: %v", err)
		return
	}
	for id, device := range m.devices {
		device.Availability, device.ReasonCode, device.Reason = DeviceDisconnected, ReasonDeviceDisconnected, "keyboard is disconnected"
		m.devices[id] = device
	}
	for _, device := range current {
		m.devices[device.ID] = device
	}
	m.writeDeviceRegistry()
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
	found, err := listKeyboards()
	if err != nil {
		return nil, err
	}
	identities := make(map[string]int, len(found))
	for _, device := range found {
		identities[device.Identity]++
	}
	devices := make([]Device, 0, len(found))
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
		devices = append(devices, Device{
			ID: opaqueDeviceID(identity), DisplayName: device.DisplayName,
			Vendor: device.Vendor, Product: device.Product, Serial: device.Serial,
			Availability: DeviceConnected, IdentityStability: stability,
			ConfiguredBy: []string{}, ReasonCode: ReasonDeviceConnected,
			Reason: "keyboard is connected and accessible",
		})
	}
	sort.Slice(devices, func(i, j int) bool { return devices[i].ID < devices[j].ID })
	return devices, nil
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
	m := &manager{devices: make(map[string]Device), deviceRegistryPath: filepath.Join(base, "devices.json")}
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
	fmt.Fprintln(writer, "ID\tNAME\tVENDOR\tPRODUCT\tSERIAL\tIDENTITY")
	for _, device := range devices {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\n", device.ID, device.DisplayName, device.Vendor, device.Product, device.Serial, device.IdentityStability)
	}
	_ = writer.Flush()
	return 0
}
