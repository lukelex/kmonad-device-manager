package manager

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func startTestAPIServer(t *testing.T) (string, *apiServer) {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "api.sock")
	server, err := startAPIServer(path, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go server.run(ctx)
	t.Cleanup(func() {
		cancel()
		select {
		case <-server.finished:
		case <-time.After(time.Second):
			t.Error("API server did not stop")
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
