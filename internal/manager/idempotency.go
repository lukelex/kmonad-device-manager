package manager

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

const (
	idempotencyStoreVersion = 1
	maxIdempotencyKeyBytes  = 128
	maxIdempotencyRecords   = 128
)

func isIdempotentMutationMethod(method string) bool {
	switch method {
	case "configuration.apply", "configuration.create", "configuration.update", "configuration.adopt", "configuration.set_enabled", "configuration.delete":
		return true
	}
	return false
}

// Records contain public operations and hashes only, never caller paths,
// profile content, raw idempotency keys, or generated configuration paths.
type idempotencyRecord struct {
	Fingerprint string    `json:"fingerprint"`
	Operation   Operation `json:"operation"`
	Error       *apiError `json:"error,omitempty"`
	Finished    bool      `json:"finished"`
}

type idempotencyStore struct {
	Version int                          `json:"version"`
	Records map[string]idempotencyRecord `json:"records"`
}

func idempotencyIdentity(request apiRequest) (string, string, *apiError) {
	if len(request.IdempotencyKey) == 0 || len(request.IdempotencyKey) > maxIdempotencyKeyBytes || strings.TrimSpace(request.IdempotencyKey) == "" {
		return "", "", &apiError{Code: "invalid_request", Message: "a non-empty idempotency_key of at most 128 bytes is required"}
	}
	var params any
	decoder := json.NewDecoder(bytes.NewReader(request.Params))
	decoder.UseNumber()
	if err := decoder.Decode(&params); err != nil {
		return "", "", &apiError{Code: "invalid_request", Message: "invalid mutation parameters"}
	}
	canonical, err := json.Marshal(params)
	if err != nil {
		return "", "", &apiError{Code: "invalid_request", Message: "invalid mutation parameters"}
	}
	key := sha256.Sum256([]byte(request.IdempotencyKey))
	fingerprint := sha256.Sum256(append(append([]byte(request.Method), 0), canonical...))
	return hex.EncodeToString(key[:]), hex.EncodeToString(fingerprint[:]), nil
}

func (m *manager) loadIdempotencyRecords() error {
	m.idempotencyRecords = make(map[string]idempotencyRecord)
	data, err := readFileLimited(m.idempotencyPath, 4<<20)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var store idempotencyStore
	if err := json.Unmarshal(data, &store); err != nil {
		return err
	}
	if store.Version != idempotencyStoreVersion || len(store.Records) > maxIdempotencyRecords {
		return fmt.Errorf("unsupported or oversized idempotency journal")
	}
	validated := make(map[string]idempotencyRecord, len(store.Records))
	for key, record := range store.Records {
		if len(key) != 64 || len(record.Fingerprint) != 64 || record.Operation.ID == "" {
			return fmt.Errorf("invalid idempotency journal entry")
		}
		if !record.Finished {
			record.Operation.State = OperationFailed
			record.Operation.ReasonCode = ReasonInternal
			record.Operation.Reason = "manager restarted before mutation outcome was durably recorded; inspect the configuration snapshot"
			record.Operation.UpdatedAt = time.Now()
			record.Finished = true
		}
		validated[key] = record
	}
	// Never re-execute an accepted mutation on recovery: its filesystem side
	// effects may already have occurred before the manager stopped.
	if err := m.persistIdempotencyRecords(validated); err != nil {
		return err
	}
	m.idempotencyRecords = validated
	if m.operations == nil {
		m.operations = make(map[string]Operation)
	}
	for _, record := range validated {
		m.operations[record.Operation.ID] = record.Operation
	}
	return nil
}

func (m *manager) persistIdempotencyRecords(records map[string]idempotencyRecord) error {
	if m.idempotencyPath == "" {
		return fmt.Errorf("idempotency journal is unavailable")
	}
	data, err := json.Marshal(idempotencyStore{Version: idempotencyStoreVersion, Records: records})
	if err != nil {
		return err
	}
	return writeAtomicPrivateFile(m.idempotencyPath, append(data, '\n'), 0o600)
}

func (m *manager) operationHasIdempotencyRecord(id string) bool {
	for _, record := range m.idempotencyRecords {
		if record.Operation.ID == id {
			return true
		}
	}
	return false
}

type mutationAdmission struct {
	key    string
	replay bool
	record idempotencyRecord
}

func (m *manager) admitIdempotentMutation(key, fingerprint string, method ...string) commandResult {
	if m.idempotencyPath == "" {
		return commandResult{err: &apiError{Code: "temporary_unavailable", Message: "durable mutation journal is unavailable"}}
	}
	if m.operations == nil {
		m.operations = make(map[string]Operation)
	}
	if record, found := m.idempotencyRecords[key]; found {
		if record.Fingerprint != fingerprint {
			return commandResult{err: &apiError{Code: "idempotency_conflict", Message: "idempotency key was used for a different mutation"}}
		}
		if !record.Finished {
			if current, exists := m.operations[record.Operation.ID]; exists && current.State == OperationRunning {
				record.Operation = current
			}
		}
		return commandResult{result: mutationAdmission{key: key, replay: true, record: record}}
	}
	updated := make(map[string]idempotencyRecord, len(m.idempotencyRecords)+1)
	for id, record := range m.idempotencyRecords {
		updated[id] = record
	}
	var evicted string
	if len(updated) >= maxIdempotencyRecords {
		keys := make([]string, 0, len(updated))
		for id, record := range updated {
			if record.Finished {
				keys = append(keys, id)
			}
		}
		sort.Slice(keys, func(i, j int) bool {
			left, right := updated[keys[i]].Operation.UpdatedAt, updated[keys[j]].Operation.UpdatedAt
			if left.Equal(right) {
				return keys[i] < keys[j]
			}
			return left.Before(right)
		})
		if len(keys) == 0 {
			return commandResult{err: &apiError{Code: "resource_exhausted", Message: "too many pending durable mutations"}}
		}
		evicted = keys[0]
		delete(updated, evicted)
	}
	id, err := newOperationID()
	if err != nil {
		return commandResult{err: &apiError{Code: "temporary_unavailable", Message: "cannot allocate operation ID"}}
	}
	now := time.Now()
	kind := OperationApply
	if len(method) != 0 {
		switch method[0] {
		case "configuration.adopt":
			kind = OperationAdopt
		case "configuration.set_enabled", "configuration.delete":
			kind = OperationLifecycle
		}
	}
	record := idempotencyRecord{Fingerprint: fingerprint, Operation: Operation{
		ID: id, Kind: kind, State: OperationRunning, StartedAt: now, UpdatedAt: now,
		ReasonCode: ReasonOperationRunning, Reason: "mutation accepted by manager",
	}}
	updated[key] = record
	if err := m.persistIdempotencyRecords(updated); err != nil {
		return commandResult{err: &apiError{Code: "temporary_unavailable", Message: "cannot persist mutation admission"}}
	}
	if evicted != "" {
		delete(m.operations, m.idempotencyRecords[evicted].Operation.ID)
	}
	m.idempotencyRecords = updated
	m.operations[id] = record.Operation
	return commandResult{result: mutationAdmission{key: key, record: record}}
}

func (m *manager) completeIdempotentMutation(key string, result commandResult) commandResult {
	record := m.idempotencyRecords[key]
	if response, ok := result.result.(map[string]Operation); ok && response["operation"].ID == record.Operation.ID {
		record.Operation = response["operation"]
	} else {
		if result.err == nil {
			result.err = &apiError{Code: "internal", Message: "mutation returned an inconsistent operation"}
		}
		record.Operation.State = OperationFailed
		record.Operation.ReasonCode = ReasonInternal
		if result.err.Code == "stale_revision" {
			record.Operation.ReasonCode = ReasonConfigurationRevisionStale
		}
		record.Operation.Reason = "mutation failed before completion: " + result.err.Code
		record.Operation.UpdatedAt = time.Now()
	}
	record.Error = result.err
	record.Finished = true
	updated := make(map[string]idempotencyRecord, len(m.idempotencyRecords))
	for id, entry := range m.idempotencyRecords {
		updated[id] = entry
	}
	updated[key] = record
	if err := m.persistIdempotencyRecords(updated); err != nil {
		return commandResult{err: &apiError{Code: "temporary_unavailable", Message: "mutation outcome could not be durably recorded; retry with the same key"}}
	}
	m.idempotencyRecords = updated
	m.operations[record.Operation.ID] = record.Operation
	return result
}

// Once admitted, execution uses the manager lifetime rather than the client
// socket or request deadline. A duplicate during execution reads the same ID.
func runIdempotentMutation(ctx context.Context, owner *manager, request apiRequest, execute func(context.Context, string) commandResult) commandResult {
	key, fingerprint, requestErr := idempotencyIdentity(request)
	if requestErr != nil {
		return commandResult{err: requestErr}
	}
	admitted := owner.submitCommand(ctx, func(_ context.Context, m *manager) commandResult {
		return m.admitIdempotentMutation(key, fingerprint, request.Method)
	})
	if admitted.err != nil {
		return admitted
	}
	entry := admitted.result.(mutationAdmission)
	if entry.replay {
		if entry.record.Error != nil {
			return commandResult{err: entry.record.Error}
		}
		return commandResult{result: map[string]Operation{"operation": entry.record.Operation}}
	}
	managerContext := owner.runContext
	if managerContext == nil {
		managerContext = context.Background()
	}
	result := execute(managerContext, entry.record.Operation.ID)
	return owner.submitCommand(managerContext, func(_ context.Context, m *manager) commandResult {
		return m.completeIdempotentMutation(key, result)
	})
}
