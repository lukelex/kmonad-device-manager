package manager

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type configurationSetEnabledParams struct {
	ConfigurationID  string `json:"configuration_id"`
	ExpectedRevision uint64 `json:"expected_revision"`
	Enabled          bool   `json:"enabled"`
}

type configurationDeleteParams struct {
	ConfigurationID  string `json:"configuration_id"`
	ExpectedRevision uint64 `json:"expected_revision"`
}

func (m *manager) setManagedConfigurationEnabled(ctx context.Context, params configurationSetEnabledParams) commandResult {
	if ctx.Err() != nil {
		return commandDeadlineResult()
	}
	configuration, result := m.currentManagedConfiguration(params.ConfigurationID, params.ExpectedRevision)
	if result.err != nil {
		return result
	}
	if m.operations == nil {
		m.operations = make(map[string]Operation)
	}
	if configuration.Enabled == params.Enabled {
		operation := m.newLifecycleOperation(configuration, "configuration is already in the requested lifecycle state")
		m.operations[operation.ID] = operation
		m.pruneOperations()
		return commandResult{result: map[string]Operation{"operation": operation}}
	}
	configuration.Enabled = params.Enabled
	configuration.Revision++
	if err := m.storeManagedConfigurationMetadata(configuration); err != nil {
		return commandResult{err: &apiError{Code: "temporary_unavailable", Message: "cannot persist managed configuration lifecycle state"}}
	}
	m.reconcile(time.Now())
	reason := "configuration enabled; it will recover automatically when its keyboard is available"
	if !params.Enabled {
		reason = "configuration disabled and its KMonad process stopped"
	}
	operation := m.newLifecycleOperation(configuration, reason)
	m.operations[operation.ID] = operation
	m.pruneOperations()
	return commandResult{result: map[string]Operation{"operation": operation}}
}

func (m *manager) deleteManagedConfiguration(ctx context.Context, params configurationDeleteParams) commandResult {
	if ctx.Err() != nil {
		return commandDeadlineResult()
	}
	configuration, result := m.currentManagedConfiguration(params.ConfigurationID, params.ExpectedRevision)
	if result.err != nil {
		return result
	}
	if m.operations == nil {
		m.operations = make(map[string]Operation)
	}
	if err := m.removeManagedConfigurationMetadata(configuration.ID); err != nil {
		return commandResult{err: &apiError{Code: "temporary_unavailable", Message: "cannot remove managed configuration metadata"}}
	}
	m.reconcile(time.Now())
	// The ID is generated and validated before use, so this removes only the
	// manager-owned revision directory for the requested configuration.
	if err := os.RemoveAll(filepath.Join(m.managedConfigDir, configuration.ID)); err != nil {
		return commandResult{err: &apiError{Code: "internal", Message: "configuration was stopped but its stored revisions could not be removed"}}
	}
	if err := syncDirectory(m.managedConfigDir); err != nil {
		return commandResult{err: &apiError{Code: "internal", Message: "configuration was stopped but its deletion could not be synchronized"}}
	}
	operation := m.newLifecycleOperation(configuration, "configuration deleted and its KMonad process stopped")
	operation.ConfigurationRevision = 0
	m.operations[operation.ID] = operation
	m.pruneOperations()
	return commandResult{result: map[string]Operation{"operation": operation}}
}

func (m *manager) currentManagedConfiguration(id string, expectedRevision uint64) (managedConfiguration, commandResult) {
	if !validConfigurationID(id) {
		return managedConfiguration{}, commandResult{err: &apiError{Code: "not_found", Message: "managed configuration does not exist"}}
	}
	configuration, exists := m.managedConfigs[id]
	if !exists {
		return managedConfiguration{}, commandResult{err: &apiError{Code: "not_found", Message: "managed configuration does not exist"}}
	}
	if expectedRevision == 0 {
		return managedConfiguration{}, commandResult{err: &apiError{Code: "invalid_request", Message: "expected_revision is required"}}
	}
	if configuration.Revision != expectedRevision {
		return managedConfiguration{}, commandResult{err: &apiError{Code: "stale_revision", Message: "managed configuration changed; refresh it before retrying"}}
	}
	return configuration, commandResult{}
}

func (m *manager) newLifecycleOperation(configuration managedConfiguration, reason string) Operation {
	id, err := newOperationID()
	if err != nil {
		id = fmt.Sprintf("op_lifecycle_%d", time.Now().UnixNano())
	}
	now := time.Now()
	return Operation{ID: id, Kind: OperationLifecycle, State: OperationSucceeded,
		Resource:  ResourceRef{Kind: ResourceConfiguration, ID: configuration.ID},
		StartedAt: now, UpdatedAt: now, ReasonCode: ReasonOperationSucceeded, Reason: reason,
		ConfigurationRevision: configuration.Revision}
}
