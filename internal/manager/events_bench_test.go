package manager

import (
	"fmt"
	"testing"
)

func benchmarkPublicStateManager(resourceCount int) *manager {
	m := &manager{
		devices:            make(map[string]Device, resourceCount),
		managedConfigs:     make(map[string]managedConfiguration),
		externalConfigs:    make(map[string]externalConfiguration, resourceCount),
		states:             make(map[string]*configState),
		operations:         make(map[string]Operation),
		managedTampered:    make(map[string]bool),
		idempotencyRecords: make(map[string]idempotencyRecord),
	}
	for index := 0; index < resourceCount; index++ {
		id := fmt.Sprintf("dev_%04d", index)
		m.devices[id] = Device{ID: id, DisplayName: "benchmark keyboard", Role: DeviceRoleInput,
			Availability: DeviceConnected, IdentityStability: IdentitySerial,
			ConfiguredBy: []string{}, ReasonCode: ReasonDeviceConnected, Reason: "connected"}
		configurationID := fmt.Sprintf("cfg_%04d", index)
		path := fmt.Sprintf("/benchmark/config-%04d.kbd", index)
		m.externalConfigs[configurationID] = externalConfiguration{ID: configurationID, Name: configurationID,
			Ownership: ConfigurationExternal, Path: path, DeviceID: id}
		m.states[path] = &configState{phase: phaseRunning, deviceID: id}
	}
	return m
}

func BenchmarkPublicStateSnapshotAndDiff(b *testing.B) {
	for _, resourceCount := range []int{16, 128, 512} {
		b.Run(fmt.Sprintf("resources-%d", resourceCount), func(b *testing.B) {
			m := benchmarkPublicStateManager(resourceCount)
			b.ReportAllocs()
			b.ResetTimer()
			for index := 0; index < b.N; index++ {
				before := m.lastPublicState
				after := m.capturePublicState()
				m.lastPublicState = after
				m.publishStateChangesBetween(before, after)
			}
		})
	}
}
