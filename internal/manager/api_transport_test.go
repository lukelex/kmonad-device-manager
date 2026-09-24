package manager

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/lukelex/kmonad-device-manager/internal/platform"
)

type testKeypressObserver struct{ results <-chan error }

func (observer testKeypressObserver) WaitForKeypress(ctx context.Context) error {
	select {
	case err := <-observer.results:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func startTestAPIServer(t *testing.T) (string, *apiServer) {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "api.sock")
	owner := &manager{commands: make(chan managerCommand, managerCommandQueueSize)}
	ctx, cancel := context.WithCancel(context.Background())
	owner.runContext = ctx
	commandDone := make(chan struct{})
	go func() {
		defer close(commandDone)
		for {
			select {
			case <-ctx.Done():
				return
			case command := <-owner.commands:
				owner.executeCommand(command)
			}
		}
	}()
	server, err := startAPIServer(path, "test-version", owner)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	go server.run(ctx)
	t.Cleanup(func() {
		cancel()
		select {
		case <-server.finished:
		case <-time.After(time.Second):
			t.Error("API server did not stop")
		}
		select {
		case <-commandDone:
		case <-time.After(time.Second):
			t.Error("test command owner did not stop")
		}
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Errorf("API socket was not removed: %v", err)
		}
	})
	return path, server
}

func dialAPI(t *testing.T, path string) (*bufio.Reader, net.Conn) {
	t.Helper()
	connection, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	return bufio.NewReader(connection), connection
}

func writeAPIRequest(t *testing.T, connection net.Conn, request string) {
	t.Helper()
	if _, err := connection.Write([]byte(request + "\n")); err != nil {
		t.Fatal(err)
	}
}

func readAPIResponse(t *testing.T, reader *bufio.Reader) apiResponse {
	t.Helper()
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var response apiResponse
	if err := json.Unmarshal(line, &response); err != nil {
		t.Fatal(err)
	}
	return response
}

func readAPIEvent(t *testing.T, reader *bufio.Reader) Event {
	t.Helper()
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var frame struct {
		Type string `json:"type"`
		Event
	}
	if err := json.Unmarshal(line, &frame); err != nil {
		t.Fatal(err)
	}
	if frame.Type != "event" {
		t.Fatalf("expected event frame, got %s: %s", frame.Type, line)
	}
	return frame.Event
}

func TestAPIServerNegotiatesAndServesSnapshots(t *testing.T) {
	path, _ := startTestAPIServer(t)
	reader, connection := dialAPI(t, path)
	writeAPIRequest(t, connection, `{"type":"request","id":"hello","method":"session.hello","params":{"supported_versions":[1],"client":{"name":"test","version":"1"}}}`)
	hello := readAPIResponse(t, reader)
	if hello.Error != nil || hello.ID != "hello" {
		t.Fatalf("unexpected hello response: %#v", hello)
	}
	result, ok := hello.Result.(map[string]any)
	if !ok || result["selected_version"] != float64(1) || result["manager_version"] != "test-version" || result["server_id"] == "" {
		t.Fatalf("unexpected hello result: %#v", hello.Result)
	}
	writeAPIRequest(t, connection, `{"type":"request","id":"snapshot","method":"snapshot.get","params":{}}`)
	response := readAPIResponse(t, reader)
	result, ok = response.Result.(map[string]any)
	revision, revisionOK := result["state_revision"].(float64)
	cursor, cursorOK := result["event_cursor"].(map[string]any)
	diagnostics, diagnosticsOK := result["diagnostics"].([]any)
	if response.ID != "snapshot" || response.Error != nil || !ok || !revisionOK || revision == 0 || result["health"] == nil || !cursorOK || cursor["server_id"] == "" || !diagnosticsOK || len(diagnostics) == 0 {
		t.Fatalf("snapshot request did not return authoritative state: %#v", response)
	}
	writeAPIRequest(t, connection, `{"type":"request","id":"manager","method":"manager.get","params":{}}`)
	response = readAPIResponse(t, reader)
	managerInfo, managerInfoOK := response.Result.(map[string]any)
	capabilities, capabilitiesOK := managerInfo["capabilities"].([]any)
	limits, limitsOK := managerInfo["limits"].(map[string]any)
	managerCursor, managerCursorOK := managerInfo["event_cursor"].(map[string]any)
	kmonad, kmonadOK := managerInfo["kmonad"].(map[string]any)
	limitations, limitationsOK := managerInfo["limitations"].([]any)
	if response.ID != "manager" || response.Error != nil || !managerInfoOK || managerInfo["manager_version"] != "test-version" || managerInfo["server_id"] == "" || managerInfo["platform"] != "linux" || managerInfo["platform_version"] == "" || managerInfo["backend"] != "linux-evdev" || managerInfo["backend_version"] != "evdev" || !kmonadOK || kmonad["compatibility"] != string(KMonadCompatibilityUnavailable) || !capabilitiesOK || len(capabilities) != 10 || !limitationsOK || len(limitations) != 4 || !limitsOK || limits["event_history"] != float64(maxRetainedEvents) || !managerCursorOK || managerCursor["server_id"] != managerInfo["server_id"] || managerInfo["health"] == nil {
		t.Fatalf("manager.get did not return public manager metadata: %#v", response)
	}
	writeAPIRequest(t, connection, `{"type":"request","id":"devices","method":"device.list","params":{}}`)
	response = readAPIResponse(t, reader)
	if response.ID != "devices" || response.Error != nil {
		t.Fatalf("device discovery request failed: %#v", response)
	}
}

func TestAPIServerReplaysOrderedEvents(t *testing.T) {
	path, server := startTestAPIServer(t)
	if result := server.owner.submitCommand(context.Background(), func(_ context.Context, m *manager) commandResult {
		m.publishEvent(EventDeviceAdded, ResourceRef{Kind: ResourceDevice, ID: "dev_one"}, ReasonDeviceConnected, map[string]any{})
		m.publishEvent(EventDeviceAvailabilityChanged, ResourceRef{Kind: ResourceDevice, ID: "dev_one"}, ReasonDeviceDisconnected, map[string]any{})
		return commandResult{}
	}); result.err != nil {
		t.Fatalf("could not publish test events: %#v", result)
	}
	reader, connection := dialAPI(t, path)
	writeAPIRequest(t, connection, `{"type":"request","id":"hello","method":"session.hello","params":{"supported_versions":[1]}}`)
	if response := readAPIResponse(t, reader); response.Error != nil {
		t.Fatalf("hello failed: %#v", response)
	}
	writeAPIRequest(t, connection, `{"type":"request","id":"events","method":"events.subscribe","params":{"after_event_id":0}}`)
	if response := readAPIResponse(t, reader); response.ID != "events" || response.Error != nil {
		t.Fatalf("event subscription failed: %#v", response)
	}
	first, second := readAPIEvent(t, reader), readAPIEvent(t, reader)
	if first.EventID != 1 || second.EventID != 2 || first.Type != EventDeviceAdded || second.Type != EventDeviceAvailabilityChanged || first.StateRevision >= second.StateRevision {
		t.Fatalf("events were not ordered public transitions: %#v %#v", first, second)
	}
	if result := server.owner.submitCommand(context.Background(), func(_ context.Context, m *manager) commandResult {
		m.publishEvent(EventOperationChanged, ResourceRef{Kind: ResourceOperation, ID: "op_one"}, ReasonOperationSucceeded, map[string]any{})
		return commandResult{}
	}); result.err != nil {
		t.Fatalf("could not publish a live test event: %#v", result)
	}
	live := readAPIEvent(t, reader)
	if live.EventID != 3 || live.Type != EventOperationChanged || live.StateRevision <= second.StateRevision {
		t.Fatalf("subscription did not follow live events: %#v", live)
	}
	writeAPIRequest(t, connection, `{"type":"request","id":"stale","method":"events.subscribe","params":{"after_event_id":3,"after_server_id":"srv_previous"}}`)
	if response := readAPIResponse(t, reader); response.ID != "stale" || response.Error != nil {
		t.Fatalf("stale cursor subscription was rejected instead of resynchronized: %#v", response)
	}
	if resync := readAPIEvent(t, reader); resync.Type != EventManagerResyncRequired || resync.ReasonCode != ReasonManagerResyncRequired {
		t.Fatalf("server mismatch did not force resynchronization: %#v", resync)
	}
}

func TestAPIServerRequiresHelloAndRejectsUnsupportedVersions(t *testing.T) {
	t.Run("hello first", func(t *testing.T) {
		path, _ := startTestAPIServer(t)
		reader, connection := dialAPI(t, path)
		writeAPIRequest(t, connection, `{"type":"request","id":"snapshot","method":"snapshot.get","params":{}}`)
		response := readAPIResponse(t, reader)
		if response.Error == nil || response.Error.Code != "invalid_request" {
			t.Fatalf("unexpected response: %#v", response)
		}
	})
	t.Run("supported version", func(t *testing.T) {
		path, _ := startTestAPIServer(t)
		reader, connection := dialAPI(t, path)
		writeAPIRequest(t, connection, `{"type":"request","id":"hello","method":"session.hello","params":{"supported_versions":[2]}}`)
		response := readAPIResponse(t, reader)
		if response.Error == nil || response.Error.Code != "unsupported_version" {
			t.Fatalf("unexpected response: %#v", response)
		}
	})
}

func TestAPIIdentificationSupportsCancellationAndRejectsConcurrentSessions(t *testing.T) {
	previousKeyboards := listKeyboards
	previousObserver := keypressObserver
	previousAvailability := identificationDeviceAvailability
	defer func() {
		listKeyboards = previousKeyboards
		keypressObserver = previousObserver
		identificationDeviceAvailability = previousAvailability
	}()
	results := make(chan error)
	listKeyboards = func() ([]platform.KeyboardDevice, error) {
		return []platform.KeyboardDevice{{
			Identity: "topology:test", IdentityStability: "topology", NodePath: "/dev/null",
			Availability: platform.DeviceConnected, DisplayName: "Keyboard",
		}}, nil
	}
	keypressObserver = func(string) (platform.KeypressObserver, error) { return testKeypressObserver{results: results}, nil }

	path, _ := startTestAPIServer(t)
	reader, connection := dialAPI(t, path)
	writeAPIRequest(t, connection, `{"type":"request","id":"hello","method":"session.hello","params":{"supported_versions":[1]}}`)
	_ = readAPIResponse(t, reader)
	deviceID := opaqueDeviceID("topology:test")
	writeAPIRequest(t, connection, `{"type":"request","id":"start","method":"device.identify.start","params":{"device_id":"`+deviceID+`","timeout_ms":1000}}`)
	started := readAPIResponse(t, reader)
	operation := operationFromResult(t, started)
	if operation.State != OperationWaiting {
		t.Fatalf("unexpected operation: %#v", operation)
	}
	writeAPIRequest(t, connection, `{"type":"request","id":"concurrent","method":"device.identify.start","params":{"device_id":"`+deviceID+`"}}`)
	if response := readAPIResponse(t, reader); response.Error == nil || response.Error.Code != "conflict" {
		t.Fatalf("concurrent identification was accepted: %#v", response)
	}
	writeAPIRequest(t, connection, `{"type":"request","id":"cancel","method":"device.identify.cancel","params":{"operation_id":"`+operation.ID+`"}}`)
	cancelled := operationFromResult(t, readAPIResponse(t, reader))
	if cancelled.State != OperationCancelled || cancelled.ReasonCode != ReasonOperationCancelled {
		t.Fatalf("unexpected cancelled operation: %#v", cancelled)
	}
	writeAPIRequest(t, connection, `{"type":"request","id":"status","method":"operation.get","params":{"operation_id":"`+operation.ID+`"}}`)
	if current := operationFromResult(t, readAPIResponse(t, reader)); current.State != OperationCancelled {
		t.Fatalf("cancelled operation was not retained: %#v", current)
	}

	writeAPIRequest(t, connection, `{"type":"request","id":"timeout-start","method":"device.identify.start","params":{"device_id":"`+deviceID+`","timeout_ms":1000}}`)
	timedOut := operationFromResult(t, readAPIResponse(t, reader))
	results <- context.DeadlineExceeded
	if completed := waitForOperation(t, reader, connection, timedOut.ID, OperationFailed); completed.ReasonCode != ReasonOperationTimedOut {
		t.Fatalf("unexpected timed-out operation: %#v", completed)
	}

	writeAPIRequest(t, connection, `{"type":"request","id":"hotplug-start","method":"device.identify.start","params":{"device_id":"`+deviceID+`","timeout_ms":1000}}`)
	hotplugged := operationFromResult(t, readAPIResponse(t, reader))
	identificationDeviceAvailability = func(string) platform.DeviceAvailability { return platform.DeviceDisconnected }
	results <- errors.New("device disappeared")
	if completed := waitForOperation(t, reader, connection, hotplugged.ID, OperationFailed); completed.ReasonCode != ReasonDeviceDisconnected {
		t.Fatalf("unexpected hotplug operation: %#v", completed)
	}
}

func TestAPIValidationPreviewReturnsStructuredResult(t *testing.T) {
	previousKeyboards := listKeyboards
	defer func() { listKeyboards = previousKeyboards }()
	runtime := t.TempDir()
	if err := os.Chmod(runtime, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	keyboard := platform.KeyboardDevice{Identity: "topology:preview", IdentityStability: "topology", NodePath: "/dev/null", Availability: platform.DeviceConnected}
	listKeyboards = func() ([]platform.KeyboardDevice, error) { return []platform.KeyboardDevice{keyboard}, nil }
	path, server := startTestAPIServer(t)
	server.owner.kmonadCommand = fakeKMonad(t)
	server.owner.maxConfigBytes = defaultMaxConfigBytes
	server.owner.dryRunTimeout = time.Second
	reader, connection := dialAPI(t, path)
	writeAPIRequest(t, connection, `{"type":"request","id":"hello","method":"session.hello","params":{"supported_versions":[1]}}`)
	_ = readAPIResponse(t, reader)
	deviceID := opaqueDeviceID(keyboard.Identity)
	writeAPIRequest(t, connection, `{"type":"request","id":"preview","method":"validation.preview","params":{"model":{"device_id":"`+deviceID+`","behavior":"(defsrc a)"}}}`)
	response := readAPIResponse(t, reader)
	validation := validationFromResult(t, response)
	if validation.Outcome != ValidationValid || validation.ReasonCode != ReasonValidationSucceeded {
		t.Fatalf("unexpected preview result: %#v", validation)
	}
	if validation.CandidateDigest != submittedBehaviorDigest("(defsrc a)") {
		t.Fatalf("preview digest = %q", validation.CandidateDigest)
	}
}

func TestAPIConfigurationApplyPersistsAndReportsCompletion(t *testing.T) {
	previousKeyboards := listKeyboards
	defer func() { listKeyboards = previousKeyboards }()
	runtime := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	keyboard := platform.KeyboardDevice{Identity: "topology:apply", IdentityStability: "topology", NodePath: "/dev/null", Availability: platform.DeviceConnected}
	listKeyboards = func() ([]platform.KeyboardDevice, error) { return []platform.KeyboardDevice{keyboard}, nil }
	path, server := startTestAPIServer(t)
	server.owner.configDir = t.TempDir()
	server.owner.kmonadCommand = fakeKMonad(t)
	server.owner.maxConfigBytes = defaultMaxConfigBytes
	server.owner.maxConfigs = 128
	server.owner.stopTimeout = time.Second
	server.owner.dryRunTimeout = time.Second
	server.owner.watchdogTimeout = time.Second
	server.owner.states = make(map[string]*configState)
	server.owner.duplicates = make(map[string]string)
	server.owner.devices = make(map[string]Device)
	server.owner.operations = make(map[string]Operation)
	t.Cleanup(server.owner.cleanup)
	if err := server.owner.openManagedConfigurationStore(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	reader, connection := dialAPI(t, path)
	writeAPIRequest(t, connection, `{"type":"request","id":"hello","method":"session.hello","params":{"supported_versions":[1]}}`)
	_ = readAPIResponse(t, reader)
	writeAPIRequest(t, connection, `{"type":"request","id":"apply","method":"configuration.apply","idempotency_key":"test-apply","params":{"name":"Keyboard","model":{"device_id":"`+opaqueDeviceID(keyboard.Identity)+`","behavior":"(defsrc a)"}}}`)
	response := readAPIResponse(t, reader)
	operation := operationFromResult(t, response)
	if operation.Kind != OperationApply || operation.State != OperationSucceeded || operation.ReasonCode != ReasonOperationSucceeded {
		t.Fatalf("unexpected apply operation: %#v", operation)
	}
	starts := server.owner.starts.Load()
	writeAPIRequest(t, connection, `{"type":"request","id":"replay-apply","method":"configuration.apply","idempotency_key":"test-apply","params":{"model":{"behavior":"(defsrc a)","device_id":"`+opaqueDeviceID(keyboard.Identity)+`"},"name":"Keyboard"}}`)
	if replay := operationFromResult(t, readAPIResponse(t, reader)); replay.ID != operation.ID || server.owner.starts.Load() != starts {
		t.Fatalf("apply retry caused another process transition: %#v", replay)
	}
	writeAPIRequest(t, connection, `{"type":"request","id":"disable","method":"configuration.set_enabled","idempotency_key":"test-disable","params":{"configuration_id":"`+operation.Resource.ID+`","expected_revision":`+strconv.FormatUint(operation.ConfigurationRevision, 10)+`,"enabled":false}}`)
	disabled := operationFromResult(t, readAPIResponse(t, reader))
	if disabled.Kind != OperationLifecycle || disabled.ConfigurationRevision != operation.ConfigurationRevision+1 {
		t.Fatalf("unexpected disable operation: %#v", disabled)
	}
	writeAPIRequest(t, connection, `{"type":"request","id":"replay-disable","method":"configuration.set_enabled","idempotency_key":"test-disable","params":{"enabled":false,"expected_revision":`+strconv.FormatUint(operation.ConfigurationRevision, 10)+`,"configuration_id":"`+operation.Resource.ID+`"}}`)
	if replay := operationFromResult(t, readAPIResponse(t, reader)); replay.ID != disabled.ID {
		t.Fatalf("lifecycle retry changed operation: %#v", replay)
	}
	writeAPIRequest(t, connection, `{"type":"request","id":"delete","method":"configuration.delete","idempotency_key":"test-delete","params":{"configuration_id":"`+operation.Resource.ID+`","expected_revision":`+strconv.FormatUint(disabled.ConfigurationRevision, 10)+`}}`)
	deleted := operationFromResult(t, readAPIResponse(t, reader))
	if deleted.Kind != OperationLifecycle || deleted.ConfigurationRevision != 0 {
		t.Fatalf("unexpected delete operation: %#v", deleted)
	}
	writeAPIRequest(t, connection, `{"type":"request","id":"replay-delete","method":"configuration.delete","idempotency_key":"test-delete","params":{"expected_revision":`+strconv.FormatUint(disabled.ConfigurationRevision, 10)+`,"configuration_id":"`+operation.Resource.ID+`"}}`)
	if replay := operationFromResult(t, readAPIResponse(t, reader)); replay.ID != deleted.ID {
		t.Fatalf("delete retry changed operation: %#v", replay)
	}
	writeAPIRequest(t, connection, `{"type":"request","id":"conflict","method":"configuration.delete","idempotency_key":"test-apply","params":{"expected_revision":1,"configuration_id":"`+operation.Resource.ID+`"}}`)
	if response := readAPIResponse(t, reader); response.Error == nil || response.Error.Code != "idempotency_conflict" {
		t.Fatalf("reused key did not conflict: %#v", response)
	}
	writeAPIRequest(t, connection, `{"type":"request","id":"missing-key","method":"configuration.delete","params":{"expected_revision":1,"configuration_id":"`+operation.Resource.ID+`"}}`)
	if response := readAPIResponse(t, reader); response.Error == nil || response.Error.Code != "invalid_request" {
		t.Fatalf("missing key was admitted: %#v", response)
	}
}

func TestConfigurationApplyMethodAliasesValidateTheirRequiredRevisions(t *testing.T) {
	params, apiErr := parseConfigurationApplyParams("configuration.create", json.RawMessage(`{"name":"Keyboard","model":{"device_id":"dev_1","behavior":"(defsrc a)"}}`))
	if apiErr != nil || params.Name != "Keyboard" || params.ConfigurationID != "" {
		t.Fatalf("create alias parameters = %#v, %#v", params, apiErr)
	}
	_, apiErr = parseConfigurationApplyParams("configuration.update", json.RawMessage(`{"model":{"device_id":"dev_1"}}`))
	if apiErr == nil || apiErr.Code != "invalid_request" {
		t.Fatalf("missing update revision was accepted: %#v", apiErr)
	}
}

func waitForOperation(t *testing.T, reader *bufio.Reader, connection net.Conn, operationID string, state OperationState) Operation {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		writeAPIRequest(t, connection, `{"type":"request","id":"operation","method":"operation.get","params":{"operation_id":"`+operationID+`"}}`)
		operation := operationFromResult(t, readAPIResponse(t, reader))
		if operation.State == state {
			return operation
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("operation %s did not reach %s", operationID, state)
	return Operation{}
}

func operationFromResult(t *testing.T, response apiResponse) Operation {
	t.Helper()
	if response.Error != nil {
		t.Fatalf("unexpected API error: %#v", response.Error)
	}
	data, err := json.Marshal(response.Result)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Operation Operation `json:"operation"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if result.Operation.ID == "" {
		t.Fatalf("missing operation result: %#v", response.Result)
	}
	return result.Operation
}

func validationFromResult(t *testing.T, response apiResponse) ValidationResult {
	t.Helper()
	if response.Error != nil {
		t.Fatalf("unexpected API error: %#v", response.Error)
	}
	data, err := json.Marshal(response.Result)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Validation ValidationResult `json:"validation"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result.Validation
}

func TestAPIRequestValidationBoundsFramesAndDeadlines(t *testing.T) {
	path, _ := startTestAPIServer(t)
	reader, connection := dialAPI(t, path)
	writeAPIRequest(t, connection, `{"type":"request","id":"hello","method":"session.hello","params":{"supported_versions":[1]}}`)
	_ = readAPIResponse(t, reader)
	writeAPIRequest(t, connection, `{"type":"request","id":"deadline","method":"snapshot.get","params":{},"deadline_ms":30001}`)
	response := readAPIResponse(t, reader)
	if response.ID != "deadline" || response.Error == nil || response.Error.Code != "invalid_request" {
		t.Fatalf("invalid deadline was accepted: %#v", response)
	}
	if _, err := connection.Write(append(make([]byte, apiFrameLimit+1), '\n')); err != nil {
		t.Fatal(err)
	}
	if err := connection.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ReadByte(); err == nil {
		t.Fatal("oversized API frame did not close the connection")
	}
}
