package manager

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lukelex/kmonad-device-manager/internal/platform"
)

func TestIdempotencyJournalReplaysAfterRestartAndRejectsConflicts(t *testing.T) {
	base := t.TempDir()
	m := &manager{operations: make(map[string]Operation)}
	if err := m.openManagedConfigurationStore(base); err != nil {
		t.Fatal(err)
	}
	request := apiRequest{Method: "configuration.create", IdempotencyKey: "opaque-recovery-key", Params: json.RawMessage(`{"name":"Keyboard","model":{"behavior":"(defsrc a)","device_id":"dev_one"}}`)}
	key, fingerprint, apiErr := idempotencyIdentity(request)
	if apiErr != nil {
		t.Fatal(apiErr)
	}
	admission := m.admitIdempotentMutation(key, fingerprint)
	if admission.err != nil {
		t.Fatal(admission.err)
	}
	id := admission.result.(mutationAdmission).record.Operation.ID
	completed := admission.result.(mutationAdmission).record.Operation
	completed.State = OperationSucceeded
	completed.UpdatedAt = time.Now()
	completed.Resource = ResourceRef{Kind: ResourceConfiguration, ID: "cfg_example"}
	completed.ConfigurationRevision = 1
	if result := m.completeIdempotentMutation(key, commandResult{result: map[string]Operation{"operation": completed}}); result.err != nil {
		t.Fatal(result.err)
	}
	reopened := &manager{operations: make(map[string]Operation)}
	if err := reopened.openManagedConfigurationStore(base); err != nil {
		t.Fatal(err)
	}
	request.Params = json.RawMessage(`{"model":{"device_id":"dev_one","behavior":"(defsrc a)"},"name":"Keyboard"}`)
	secondKey, secondFingerprint, apiErr := idempotencyIdentity(request)
	if apiErr != nil || secondKey != key || secondFingerprint != fingerprint {
		t.Fatalf("JSON member order changed request identity: %q %q %v", secondKey, secondFingerprint, apiErr)
	}
	replay := reopened.admitIdempotentMutation(secondKey, secondFingerprint)
	if replay.err != nil || !replay.result.(mutationAdmission).replay || replay.result.(mutationAdmission).record.Operation.ID != id || reopened.identificationOperation(id).err != nil {
		t.Fatalf("restart lost operation: %#v", replay)
	}
	for i := 0; i < maxRetainedOperations+4; i++ {
		operationID, err := newOperationID()
		if err != nil {
			t.Fatal(err)
		}
		reopened.operations[operationID] = Operation{ID: operationID, UpdatedAt: time.Now().Add(-time.Hour)}
	}
	reopened.pruneOperations()
	if reopened.identificationOperation(id).err != nil {
		t.Fatal("operation was pruned while its idempotency key remained replayable")
	}
	other := apiRequest{Method: "configuration.delete", IdempotencyKey: request.IdempotencyKey, Params: json.RawMessage(`{"configuration_id":"cfg_example","expected_revision":1}`)}
	_, different, _ := idempotencyIdentity(other)
	if result := reopened.admitIdempotentMutation(key, different); result.err == nil || result.err.Code != "idempotency_conflict" {
		t.Fatalf("key reused across methods: %#v", result)
	}
	info, err := os.Stat(filepath.Join(base, "idempotency.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("journal permissions: %v %v", info, err)
	}
	for _, invalid := range []string{"", "  ", strings.Repeat("k", maxIdempotencyKeyBytes+1)} {
		request.IdempotencyKey = invalid
		if _, _, apiErr := idempotencyIdentity(request); apiErr == nil || apiErr.Code != "invalid_request" {
			t.Fatalf("accepted invalid key %q", invalid)
		}
	}
}

func TestIdempotencyFingerprintPreservesLargeRevisions(t *testing.T) {
	request := apiRequest{Method: "configuration.update", IdempotencyKey: "revision", Params: json.RawMessage(`{"expected_revision":9007199254740992}`)}
	_, first, err := idempotencyIdentity(request)
	if err != nil {
		t.Fatal(err)
	}
	request.Params = json.RawMessage(`{"expected_revision":9007199254740993}`)
	_, second, err := idempotencyIdentity(request)
	if err != nil || first == second {
		t.Fatalf("different uint64 revisions shared a fingerprint: %q %q %v", first, second, err)
	}
}

func TestAcceptedApplySurvivesClientDisconnectAndReplays(t *testing.T) {
	previous := listKeyboards
	defer func() { listKeyboards = previous }()
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	keyboard := platform.KeyboardDevice{Identity: "topology:idempotent", IdentityStability: "topology", NodePath: "/dev/null", Availability: platform.DeviceConnected}
	listKeyboards = func() ([]platform.KeyboardDevice, error) { return []platform.KeyboardDevice{keyboard}, nil }
	path, server := startTestAPIServer(t)
	owner := server.owner
	owner.configDir = t.TempDir()
	owner.kmonadCommand = fakeKMonad(t)
	owner.maxConfigBytes, owner.maxConfigs = defaultMaxConfigBytes, 128
	owner.stopTimeout, owner.dryRunTimeout = time.Second, time.Second
	owner.states, owner.duplicates = make(map[string]*configState), make(map[string]string)
	owner.devices, owner.operations = make(map[string]Device), make(map[string]Operation)
	t.Cleanup(owner.cleanup)
	if err := owner.openManagedConfigurationStore(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	request := `{"type":"request","id":"create","method":"configuration.create","idempotency_key":"lost-response","params":{"name":"Keyboard","model":{"device_id":"` + opaqueDeviceID(keyboard.Identity) + `","behavior":"(defsrc a)"}}}`
	reader, connection := dialAPI(t, path)
	writeAPIRequest(t, connection, `{"type":"request","id":"hello","method":"session.hello","params":{"supported_versions":[1]}}`)
	_ = readAPIResponse(t, reader)
	writeAPIRequest(t, connection, request)
	waitFor(t, func() bool {
		result := owner.submitCommand(context.Background(), func(_ context.Context, m *manager) commandResult {
			return commandResult{result: len(m.idempotencyRecords) == 1}
		})
		return result.err == nil && result.result == true
	})
	_ = connection.Close()
	reader, connection = dialAPI(t, path)
	writeAPIRequest(t, connection, `{"type":"request","id":"hello","method":"session.hello","params":{"supported_versions":[1]}}`)
	_ = readAPIResponse(t, reader)
	writeAPIRequest(t, connection, request)
	first := operationFromResult(t, readAPIResponse(t, reader))
	waitFor(t, func() bool {
		result := owner.submitCommand(context.Background(), func(_ context.Context, m *manager) commandResult {
			return m.identificationOperation(first.ID)
		})
		if result.err != nil {
			return false
		}
		return result.result.(map[string]Operation)["operation"].State == OperationSucceeded
	})
	writeAPIRequest(t, connection, request)
	second := operationFromResult(t, readAPIResponse(t, reader))
	if first.ID != second.ID || len(owner.managedConfigs) != 1 {
		t.Fatalf("retry caused another apply: %#v %#v", first, second)
	}
}

func TestIdempotencyAdmissionSurvivesRestartWithoutReexecutingPendingWork(t *testing.T) {
	base := t.TempDir()
	owner := &manager{operations: make(map[string]Operation)}
	if err := owner.openManagedConfigurationStore(base); err != nil {
		t.Fatal(err)
	}
	key, fingerprint, apiErr := idempotencyIdentity(apiRequest{IdempotencyKey: "pending", Method: "configuration.delete", Params: json.RawMessage(`{"configuration_id":"cfg_missing","expected_revision":1}`)})
	if apiErr != nil {
		t.Fatal(apiErr)
	}
	accepted := owner.admitIdempotentMutation(key, fingerprint)
	if accepted.err != nil {
		t.Fatal(accepted.err)
	}
	reopened := &manager{operations: make(map[string]Operation)}
	if err := reopened.openManagedConfigurationStore(base); err != nil {
		t.Fatal(err)
	}
	replay := reopened.admitIdempotentMutation(key, fingerprint)
	if replay.err != nil || !replay.result.(mutationAdmission).replay || replay.result.(mutationAdmission).record.Operation.State != OperationFailed || replay.result.(mutationAdmission).record.Operation.ID != accepted.result.(mutationAdmission).record.Operation.ID {
		t.Fatalf("restarted mutation was re-admitted or lost: %#v", replay)
	}
	if err := reopened.openManagedConfigurationStore(base); err != nil {
		t.Fatal(err)
	}
	if reopened.identificationOperation(accepted.result.(mutationAdmission).record.Operation.ID).err != nil {
		t.Fatal("interrupted operation was not retained after a second restart")
	}
}

func TestIdempotencyJSONLinesFixture(t *testing.T) {
	data, err := os.Open(filepath.Join("..", "..", "tests", "fixtures", "idempotency-v1.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	scanner := bufio.NewScanner(data)
	var requests []apiRequest
	var responses []apiResponse
	for scanner.Scan() {
		var frame struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &frame); err != nil {
			t.Fatal(err)
		}
		if frame.Type == "request" {
			var request apiRequest
			if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
				t.Fatal(err)
			}
			requests = append(requests, request)
		} else {
			var response apiResponse
			if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			responses = append(responses, response)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 3 || len(responses) != 3 {
		t.Fatalf("fixture has %d requests and %d responses", len(requests), len(responses))
	}
	key, fingerprint, _ := idempotencyIdentity(requests[0])
	replayKey, replayFingerprint, _ := idempotencyIdentity(requests[1])
	conflictKey, conflictFingerprint, _ := idempotencyIdentity(requests[2])
	if key != replayKey || key != conflictKey || fingerprint != replayFingerprint || fingerprint == conflictFingerprint || responses[0].ID != requests[0].ID || responses[1].ID != requests[1].ID || responses[2].Error == nil || responses[2].Error.Code != "idempotency_conflict" {
		t.Fatal("fixture does not demonstrate stable replay and conflict")
	}
}
