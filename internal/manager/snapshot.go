package manager

import (
	"sort"
	"time"
)

func (m *manager) advanceStateRevision() {
	m.stateRevision++
}

func (m *manager) snapshot() Snapshot {
	// Refreshing discovery within the owner makes this a coherent current view
	// without requiring a GUI or API client for reconciliation to continue.
	m.refreshDevices()
	m.refreshExternalConfigurationRegistry()
	m.advanceStateRevision()
	return Snapshot{
		StateRevision:  m.stateRevision,
		Devices:        m.deviceList(),
		Configurations: m.configurationList(),
		Operations:     m.operationList(),
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
