package manager

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
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

func TestAPIServerNegotiatesAndRejectsUnimplementedMethods(t *testing.T) {
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
	if response.ID != "snapshot" || response.Error == nil || response.Error.Code != "unsupported_capability" {
		t.Fatalf("unimplemented method did not return a bounded capability error: %#v", response)
	}
	writeAPIRequest(t, connection, `{"type":"request","id":"devices","method":"device.list","params":{}}`)
	response = readAPIResponse(t, reader)
	if response.ID != "devices" || response.Error != nil {
		t.Fatalf("device discovery request failed: %#v", response)
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
