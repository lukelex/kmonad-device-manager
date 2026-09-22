package manager

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"text/tabwriter"
)

func discoverDevices() ([]Device, error) {
	found, err := host.ListKeyboards()
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
	devices, err := discoverDevices()
	if err != nil {
		writeCLIError(os.Stderr, jsonOutput, "device_discovery_failed", fmt.Sprintf("cannot enumerate keyboards: %v", err))
		return 1
	}
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
