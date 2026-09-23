package manager

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/lukelex/kmonad-device-manager/internal/platform"
)

const (
	defaultIdentificationTimeout = 15 * time.Second
	minIdentificationTimeout     = time.Second
	maxIdentificationTimeout     = 30 * time.Second
	maxRetainedOperations        = 128
)

type identificationSession struct {
	operationID string
	deviceID    string
	platformID  string
	nodePath    string
	cancel      context.CancelFunc
}

type identifyStartParams struct {
	DeviceID  string `json:"device_id"`
	TimeoutMS int    `json:"timeout_ms,omitempty"`
}

type identifyCancelParams struct {
	OperationID string `json:"operation_id"`
}

var keypressObserver = func(path string) (platform.KeypressObserver, error) {
	return host.KeypressObserver(path)
}

var identificationDeviceAvailability = func(path string) platform.DeviceAvailability {
	return host.DeviceAvailability(path)
}

func (m *manager) startIdentification(ctx context.Context, params identifyStartParams) commandResult {
	if ctx.Err() != nil {
		return commandDeadlineResult()
	}
	if params.DeviceID == "" {
		return commandResult{err: &apiError{Code: "invalid_request", Message: "device_id is required"}}
	}
	if m.identification != nil {
		return commandResult{err: &apiError{Code: "conflict", Message: "an identification session is already running"}}
	}
	timeout := defaultIdentificationTimeout
	if params.TimeoutMS != 0 {
		timeout = time.Duration(params.TimeoutMS) * time.Millisecond
	}
	if timeout < minIdentificationTimeout || timeout > maxIdentificationTimeout {
		return commandResult{err: &apiError{Code: "invalid_request", Message: "timeout_ms must be between 1000 and 30000"}}
	}
	m.refreshDevices()
	discovered, err := discoverKeyboardDevices()
	if err != nil {
		return commandResult{err: &apiError{Code: "temporary_unavailable", Message: "keyboard discovery is unavailable"}}
	}
	var target *discoveredKeyboard
	for index := range discovered {
		if discovered[index].device.ID == params.DeviceID {
			target = &discovered[index]
			break
		}
	}
	if target == nil {
		return commandResult{err: &apiError{Code: "not_found", Message: "device does not exist"}}
	}
	if target.device.Availability != DeviceConnected {
		return commandResult{err: &apiError{Code: "temporary_unavailable", Message: "device is not connected and accessible"}}
	}
	platformID, err := deviceID(target.nodePath)
	if err != nil {
		return commandResult{err: &apiError{Code: "temporary_unavailable", Message: "device is no longer available"}}
	}
	operationID, err := newOperationID()
	if err != nil {
		return commandResult{err: &apiError{Code: "internal", Message: "cannot create identification operation"}}
	}
	observer, err := keypressObserver(target.nodePath)
	if err != nil {
		return commandResult{err: &apiError{Code: "temporary_unavailable", Message: "device cannot be observed"}}
	}
	parent := m.runContext
	if parent == nil {
		parent = context.Background()
	}
	sessionContext, cancel := context.WithTimeout(parent, timeout)
	now := time.Now()
	operation := Operation{
		ID: operationID, Kind: OperationIdentify, State: OperationWaiting,
		Resource:  ResourceRef{Kind: ResourceDevice, ID: target.device.ID},
		StartedAt: now, UpdatedAt: now, ReasonCode: ReasonOperationRunning,
		Reason: "waiting for a keypress",
	}
	session := &identificationSession{operationID: operationID, deviceID: target.device.ID, platformID: platformID, nodePath: target.nodePath, cancel: cancel}
	if m.operations == nil {
		m.operations = make(map[string]Operation)
	}
	m.operations[operationID] = operation
	m.publishOperationChange(operation)
	m.identification = session
	m.pauseIdentificationConfigurations(platformID)
	m.writeStatus()
	go m.waitForIdentification(sessionContext, session, observer)
	return commandResult{result: map[string]Operation{"operation": operation}}
}

func (m *manager) waitForIdentification(ctx context.Context, session *identificationSession, observer platform.KeypressObserver) {
	err := observer.WaitForKeypress(ctx)
	parent := m.runContext
	if parent == nil {
		parent = context.Background()
	}
	_ = m.submitCommand(parent, func(_ context.Context, owner *manager) commandResult {
		owner.finishIdentification(session, err)
		return commandResult{}
	})
}

func (m *manager) finishIdentification(session *identificationSession, result error) {
	if session == nil || m.identification != session {
		return
	}
	operation := m.operations[session.operationID]
	operation.UpdatedAt = time.Now()
	switch {
	case result == nil:
		operation.State = OperationSucceeded
		operation.ReasonCode = ReasonOperationSucceeded
		operation.Reason = "keypress received"
	case errors.Is(result, context.DeadlineExceeded):
		operation.State = OperationFailed
		operation.ReasonCode = ReasonOperationTimedOut
		operation.Reason = "no keypress was received before the timeout"
	case errors.Is(result, context.Canceled):
		operation.State = OperationCancelled
		operation.ReasonCode = ReasonOperationCancelled
		operation.Reason = "identification was cancelled"
	default:
		operation.State = OperationFailed
		operation.ReasonCode, operation.Reason = identificationFailureReason(session)
	}
	m.operations[session.operationID] = operation
	m.publishOperationChange(operation)
	session.cancel()
	m.identification = nil
	m.pruneOperations()
	m.reconcile(time.Now())
}

func identificationFailureReason(session *identificationSession) (ReasonCode, string) {
	switch identificationDeviceAvailability(session.nodePath) {
	case platform.DeviceDisconnected:
		return ReasonDeviceDisconnected, "device disconnected during identification"
	case platform.DeviceInaccessible:
		return ReasonDeviceInaccessible, "device became inaccessible during identification"
	case platform.DeviceUnsupported:
		return ReasonDeviceUnsupported, "device became unsupported during identification"
	default:
		return ReasonInternal, "keypress observation failed"
	}
}

func (m *manager) cancelIdentificationOperation(operationID string) commandResult {
	_, exists := m.operations[operationID]
	if !exists {
		return commandResult{err: &apiError{Code: "not_found", Message: "operation does not exist"}}
	}
	if m.identification == nil || m.identification.operationID != operationID {
		return commandResult{err: &apiError{Code: "operation_not_cancellable", Message: "operation is no longer running"}}
	}
	m.identification.cancel()
	m.finishIdentification(m.identification, context.Canceled)
	return commandResult{result: map[string]Operation{"operation": m.operations[operationID]}}
}

func (m *manager) identificationOperation(operationID string) commandResult {
	operation, exists := m.operations[operationID]
	if !exists {
		return commandResult{err: &apiError{Code: "not_found", Message: "operation does not exist"}}
	}
	return commandResult{result: map[string]Operation{"operation": operation}}
}

func (m *manager) identifyingDevice(platformID string) bool {
	return m.identification != nil && m.identification.platformID == platformID
}

func (m *manager) pauseIdentificationConfigurations(platformID string) {
	deadline := time.Now().Add(m.stopTimeout)
	for config, state := range m.states {
		if state.deviceID != platformID {
			continue
		}
		m.stopProcess(config, state, deadline)
		transitionPhase(state, phaseStopped)
	}
}

func (m *manager) cancelIdentification() {
	if m.identification == nil {
		return
	}
	m.identification.cancel()
	m.identification = nil
}

func (m *manager) pruneOperations() {
	if len(m.operations) <= maxRetainedOperations {
		return
	}
	operations := make([]Operation, 0, len(m.operations))
	for _, operation := range m.operations {
		if m.identification == nil || operation.ID != m.identification.operationID {
			operations = append(operations, operation)
		}
	}
	sort.Slice(operations, func(i, j int) bool { return operations[i].UpdatedAt.Before(operations[j].UpdatedAt) })
	for len(m.operations) > maxRetainedOperations && len(operations) > 0 {
		delete(m.operations, operations[0].ID)
		operations = operations[1:]
	}
}

func newOperationID() (string, error) {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return "", fmt.Errorf("read random operation ID: %w", err)
	}
	return "op_" + hex.EncodeToString(data), nil
}
