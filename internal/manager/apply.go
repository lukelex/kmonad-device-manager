package manager

import (
	"context"
	"errors"
	"os/exec"
	"time"
)

type configurationApplyParams struct {
	ConfigurationID  string                    `json:"configuration_id,omitempty"`
	Name             string                    `json:"name,omitempty"`
	Model            ManagedConfigurationModel `json:"model"`
	ExpectedRevision *uint64                   `json:"expected_revision,omitempty"`
}

type managedApplyPreparation struct {
	configuration managedConfiguration
	previous      *managedConfiguration
	previousLive  bool
	content       []byte
	snapshotPath  string
	command       string
	timeout       time.Duration
	operationID   string
}

// prepareManagedApply runs only on the reconciliation owner. It establishes
// the target revision and records progress before the bounded KMonad dry-run
// runs outside the owner.
func (m *manager) prepareManagedApply(ctx context.Context, params configurationApplyParams) commandResult {
	if ctx.Err() != nil {
		return commandDeadlineResult()
	}
	if m.managedConfigDir == "" {
		return commandResult{err: &apiError{Code: "temporary_unavailable", Message: "managed configuration storage is unavailable"}}
	}
	configuration, exists := m.managedConfigs[params.ConfigurationID]
	var previous *managedConfiguration
	previousLive := false
	if params.ConfigurationID == "" {
		id, err := newConfigurationID()
		if err != nil {
			return commandResult{err: &apiError{Code: "internal", Message: "cannot create configuration ID"}}
		}
		if params.Name == "" {
			return commandResult{err: &apiError{Code: "invalid_request", Message: "name is required when creating a configuration"}}
		}
		configuration = managedConfiguration{Version: managedConfigurationStoreVersion, ID: id, Name: params.Name, Enabled: true}
	} else if !validConfigurationID(params.ConfigurationID) || !exists {
		return commandResult{err: &apiError{Code: "not_found", Message: "managed configuration does not exist"}}
	} else if params.ExpectedRevision == nil {
		return commandResult{err: &apiError{Code: "invalid_request", Message: "expected_revision is required when updating a configuration"}}
	} else if *params.ExpectedRevision != configuration.Revision {
		return commandResult{err: &apiError{Code: "stale_revision", Message: "managed configuration changed; refresh it before applying"}}
	}
	if exists {
		prior := configuration
		previous = &prior
		if state := m.states[m.managedConfigurationPath(prior)]; state != nil && state.process != nil {
			previousLive = m.processHealthy(m.managedConfigurationPath(prior), state.process)
		}
	}
	if params.Name != "" {
		configuration.Name = params.Name
	}
	configuration.Model = params.Model
	configuration.Revision++
	content, validation := m.renderManagedConfiguration(params.Model)
	operation := m.newApplyOperation(configuration.ID)
	operation.ConfigurationRevision = configuration.Revision
	if validation.Outcome != ValidationValid {
		return m.finishApplyValidation(operation, validation)
	}
	limit := m.maxConfigBytes
	if limit <= 0 {
		limit = defaultMaxConfigBytes
	}
	if int64(len(content)) > limit {
		return m.finishApplyValidation(operation, validationRejected(ReasonConfigurationTooLarge, "candidate exceeds the configuration size limit", "Reduce the candidate size, then retry.", nil))
	}
	if validation = m.checkCandidateInputForClaim(content, configuration.ID); validation.Outcome != ValidationValid {
		return m.finishApplyValidation(operation, validation)
	}
	snapshot, err := createValidationSnapshot(content)
	if err != nil {
		return m.finishApplyValidation(operation, validationBlocked(ReasonDependencyUnavailable, "cannot create an apply validation snapshot", "Check the manager runtime directory, then retry.", nil))
	}
	if ctx.Err() != nil {
		_ = removeConfigSnapshot(snapshot)
		return commandDeadlineResult()
	}
	configuration.Digest = configurationDigest(content)
	if m.operations == nil {
		m.operations = make(map[string]Operation)
	}
	m.operations[operation.ID] = operation
	return commandResult{result: managedApplyPreparation{
		configuration: configuration, previous: previous, previousLive: previousLive, content: content, snapshotPath: snapshot,
		command: m.kmonadCommand, timeout: m.dryRunTimeout, operationID: operation.ID,
	}}
}

func (m *manager) newApplyOperation(configurationID string) Operation {
	id, err := newOperationID()
	if err != nil {
		id = "op_apply"
	}
	now := time.Now()
	return Operation{ID: id, Kind: OperationApply, State: OperationRunning,
		Resource:  ResourceRef{Kind: ResourceConfiguration, ID: configurationID},
		StartedAt: now, UpdatedAt: now, ReasonCode: ReasonOperationRunning, Reason: "validating configuration candidate"}
}

func (m *manager) finishApplyValidation(operation Operation, validation ValidationResult) commandResult {
	operation.UpdatedAt = time.Now()
	operation.Validation = &validation
	operation.State = OperationRejected
	operation.ReasonCode = validation.ReasonCode
	operation.Reason = validation.Reason
	if m.operations == nil {
		m.operations = make(map[string]Operation)
	}
	m.operations[operation.ID] = operation
	m.pruneOperations()
	return commandResult{result: map[string]Operation{"operation": operation}}
}

func runManagedApplyValidation(ctx context.Context, preparation managedApplyPreparation) ValidationResult {
	err := dryRunContext(ctx, preparation.command, preparation.timeout, preparation.snapshotPath)
	if err == nil {
		return ValidationResult{Outcome: ValidationValid, ReasonCode: ReasonValidationSucceeded, Reason: "candidate passed KMonad validation"}
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || isDryRunTimeout(err) {
		return validationBlocked(ReasonValidationTimedOut, "KMonad validation timed out", "Retry the apply operation after validation can complete.", nil)
	}
	if errors.Is(err, exec.ErrNotFound) {
		return validationBlocked(ReasonDependencyUnavailable, "KMonad is unavailable", "Install KMonad or correct the configured command, then retry.", nil)
	}
	return validationRejected(ReasonValidationFailed, "KMonad rejected the candidate", "Correct the KMonad configuration and retry.", nil)
}

func (m *manager) finishManagedApply(ctx context.Context, preparation managedApplyPreparation, validation ValidationResult) commandResult {
	snapshotAdopted := false
	defer func() {
		if _, retained := m.prevalidated[m.managedConfigurationPath(preparation.configuration)]; !retained && !snapshotAdopted {
			_ = removeConfigSnapshot(preparation.snapshotPath)
		}
	}()
	operation, exists := m.operations[preparation.operationID]
	if !exists {
		return commandResult{err: &apiError{Code: "internal", Message: "apply operation disappeared"}}
	}
	if ctx.Err() != nil {
		validation = validationBlocked(ReasonValidationBlocked, "apply request was cancelled before persistence", "Retry the apply operation.", nil)
	}
	if validation.Outcome != ValidationValid {
		return m.finishApplyValidation(operation, validation)
	}
	// Resolve and check again at the commit point. The external dry-run is
	// intentionally outside the owner; this closes the mutable-state window
	// without re-running an unbounded command on reconciliation.
	content, current := m.renderManagedConfiguration(preparation.configuration.Model)
	if current.Outcome != ValidationValid || string(content) != string(preparation.content) {
		if current.Outcome == ValidationValid {
			current = validationBlocked(ReasonConfigurationChanged, "the keyboard input target changed during apply", "Retry the apply operation.", nil)
		}
		return m.finishApplyValidation(operation, current)
	}
	if current = m.checkCandidateInputForClaim(content, preparation.configuration.ID); current.Outcome != ValidationValid {
		return m.finishApplyValidation(operation, current)
	}
	if err := m.storeManagedConfiguration(preparation.configuration, content); err != nil {
		return m.finishApplyValidation(operation, validationBlocked(ReasonDependencyUnavailable, "cannot persist the managed configuration", "Check manager state-directory access, then retry.", nil))
	}
	path := m.managedConfigurationPath(preparation.configuration)
	if m.prevalidated == nil {
		m.prevalidated = make(map[string]*validatedConfig)
	}
	device, err := deviceFileFromData(content)
	if err != nil {
		return commandResult{err: &apiError{Code: "internal", Message: "persisted configuration has no input device"}}
	}
	m.prevalidated[path] = &validatedConfig{device: device, signature: preparation.configuration.Digest, launchPath: preparation.snapshotPath}
	m.reconcile(time.Now())
	// cmd.Start only confirms that the child was spawned. Give the child a short,
	// bounded opportunity to exec before checking its owned process identity.
	time.Sleep(10 * time.Millisecond)
	operation = m.operations[operation.ID]
	operation.UpdatedAt = time.Now()
	operation.Validation = &validation
	state := m.states[path]
	if state != nil && state.process != nil && m.processHealthy(path, state.process) {
		snapshotAdopted = state.process.launchPath == preparation.snapshotPath
		operation.State = OperationSucceeded
		operation.ReasonCode = ReasonOperationSucceeded
		operation.Reason = "configuration persisted and activation confirmed"
	} else {
		m.rollbackManagedApply(preparation, &operation)
	}
	m.operations[operation.ID] = operation
	m.pruneOperations()
	return commandResult{result: map[string]Operation{"operation": operation}}
}

func (m *manager) rollbackManagedApply(preparation managedApplyPreparation, operation *Operation) {
	operation.UpdatedAt = time.Now()
	operation.State = OperationFailed
	operation.ReasonCode = ReasonRuntimeActivationFailed
	operation.Reason = "configuration activation was not confirmed"
	newPath := m.managedConfigurationPath(preparation.configuration)
	if state := m.states[newPath]; state != nil {
		m.stopAndDelete(newPath, time.Now().Add(m.stopTimeout))
	}
	if validation := m.prevalidated[newPath]; validation != nil {
		_ = removeConfigSnapshot(validation.launchPath)
		delete(m.prevalidated, newPath)
	}
	if preparation.previous == nil {
		if err := m.removeManagedConfigurationMetadata(preparation.configuration.ID); err != nil {
			operation.ReasonCode = ReasonRuntimeRollbackFailed
			operation.Reason = "activation failed and the new configuration could not be removed"
		}
		return
	}
	if err := m.storeManagedConfigurationMetadata(*preparation.previous); err != nil {
		operation.ReasonCode = ReasonRuntimeRollbackFailed
		operation.Reason = "activation failed and the previous revision could not be restored"
		return
	}
	m.reconcile(time.Now())
	// As with replacement confirmation, cmd.Start does not guarantee that the
	// restored child has completed exec before its owned identity is inspected.
	time.Sleep(10 * time.Millisecond)
	previousPath := m.managedConfigurationPath(*preparation.previous)
	state := m.states[previousPath]
	if !preparation.previousLive || (state != nil && state.process != nil && m.processHealthy(previousPath, state.process)) {
		operation.State = OperationRolledBack
		operation.ReasonCode = ReasonRuntimeRollbackSucceeded
		operation.Reason = "activation failed; the previous revision was restored"
		operation.ConfigurationRevision = preparation.previous.Revision
		return
	}
	operation.ReasonCode = ReasonRuntimeRollbackFailed
	operation.Reason = "activation failed and the previous revision could not be restarted"
}

func applyManagedConfiguration(ctx context.Context, owner *manager, params configurationApplyParams) commandResult {
	prepared := owner.submitCommand(ctx, func(commandContext context.Context, m *manager) commandResult {
		return m.prepareManagedApply(commandContext, params)
	})
	if prepared.err != nil {
		return prepared
	}
	if operation, ok := prepared.result.(map[string]Operation); ok {
		return commandResult{result: operation}
	}
	preparation, ok := prepared.result.(managedApplyPreparation)
	if !ok {
		return commandResult{err: &apiError{Code: "internal", Message: "apply preparation failed"}}
	}
	validation := runManagedApplyValidation(ctx, preparation)
	finishContext := ctx
	if ctx.Err() != nil {
		finishContext = context.Background()
	}
	return owner.submitCommand(finishContext, func(commandContext context.Context, m *manager) commandResult {
		return m.finishManagedApply(commandContext, preparation, validation)
	})
}
