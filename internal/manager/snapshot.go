package manager

import (
	"sort"
	"time"
)

func (m *manager) advanceStateRevision() {
	m.stateRevision++
	m.publicStateRevision.Store(m.stateRevision)
}

func (m *manager) snapshot() Snapshot {
	// Refreshing discovery within the owner makes this a coherent current view
// without requiring an API client for reconciliation to continue.
	before := m.lastPublicState
	m.refreshDevices()
	m.refreshExternalConfigurationRegistry()
	state := m.capturePublicState()
	m.lastPublicState = state
	m.publishStateChangesBetween(before, state)
	m.advanceStateRevision()
	devices := make([]Device, 0, len(state.devices))
	for _, device := range state.devices {
		devices = append(devices, device)
	}
	sort.Slice(devices, func(i, j int) bool { return devices[i].ID < devices[j].ID })
	configurations := make([]Configuration, 0, len(state.configurations))
	for _, configuration := range state.configurations {
		configurations = append(configurations, configuration)
	}
	sort.Slice(configurations, func(i, j int) bool { return configurations[i].ID < configurations[j].ID })
	diagnostics := make([]Diagnostic, 0, len(state.diagnostics))
	for _, diagnostic := range state.diagnostics {
		diagnostics = append(diagnostics, diagnostic)
	}
	sort.Slice(diagnostics, func(i, j int) bool { return diagnostics[i].ID < diagnostics[j].ID })
	return Snapshot{
		StateRevision:  m.stateRevision,
		EventCursor:    EventCursor{EventID: m.nextEventID, StateRevision: m.stateRevision},
		Devices:        devices,
		Configurations: configurations,
		Operations:     m.operationList(),
		Diagnostics:    diagnostics,
		Health:         m.managerHealth(),
	}
}

func (m *manager) operationList() []Operation {
	operations := make([]Operation, 0, len(m.operations))
	for _, operation := range m.operations {
		operations = append(operations, operation)
	}
	sort.Slice(operations, func(i, j int) bool {
		if operations[i].UpdatedAt.Equal(operations[j].UpdatedAt) {
			return operations[i].ID < operations[j].ID
		}
		return operations[i].UpdatedAt.Before(operations[j].UpdatedAt)
	})
	return operations
}

func (m *manager) managerHealth() ManagerHealth {
	health := ManagerHealth{
		Healthy:             true,
		ReasonCode:          ReasonManagerHealthy,
		Reason:              "manager reconciliation owner is responsive",
		ReconcileCount:      m.reconciles.Load(),
		FailureCount:        m.failures.Load(),
		MetricsAvailable:    m.metricsServerUp.Load(),
		StatusWriteFailures: m.statusFailures.Load(),
	}
	if progress := m.lastProgress.Load(); progress != 0 {
		progressAt := time.Unix(0, progress)
		health.LastProgressAt = &progressAt
	} else {
		health.ReasonCode = ReasonManagerStarting
		health.Reason = "manager is initializing its first reconciliation"
	}
	return health
}
