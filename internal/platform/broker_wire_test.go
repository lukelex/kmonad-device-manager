package platform

import (
	"bytes"
	"errors"
	"testing"
)

func TestParseBrokerWireRequestBindsAuthenticatedUser(t *testing.T) {
	request, err := ParseBrokerWireRequest([]byte(`{"version":1,"operation":"start","request_id":"request-1","configuration_id":"configuration-1","snapshot_id":"snapshot-1"}`), "user-1000")
	if err != nil {
		t.Fatalf("ParseBrokerWireRequest() error = %v", err)
	}
	if request.UserID != "user-1000" || request.Operation != BrokerStart || request.SnapshotID != "snapshot-1" {
		t.Fatalf("ParseBrokerWireRequest() = %#v", request)
	}
}

func TestParseBrokerWireRequestRejectsUntrustedFields(t *testing.T) {
	tests := []string{
		`{"version":1,"operation":"start","request_id":"request-1","configuration_id":"configuration-1","snapshot_id":"snapshot-1","user_id":"root"}`,
		`{"version":1,"operation":"start","request_id":"request-1","configuration_id":"configuration-1","snapshot_id":"snapshot-1","command":"/bin/sh"}`,
		`{"version":1,"operation":"start","request_id":"request-1","configuration_id":"configuration-1","snapshot_id":"snapshot-1","snapshot_path":"/tmp/config.kbd"}`,
		`{"version":1,"operation":"start","request_id":"request-1","configuration_id":"configuration-1","snapshot_id":"snapshot-1"} {}`,
	}
	for _, frame := range tests {
		t.Run(frame, func(t *testing.T) {
			_, err := ParseBrokerWireRequest([]byte(frame), "user-1000")
			if !errors.Is(err, ErrBrokerMalformedRequest) {
				t.Fatalf("ParseBrokerWireRequest() error = %v, want ErrBrokerMalformedRequest", err)
			}
		})
	}
}

func TestReadBrokerFrameBoundsInput(t *testing.T) {
	reader := NewBrokerFrameReader(bytes.NewReader(append(bytes.Repeat([]byte{'x'}, PrivilegedBrokerFrameLimit+1), '\n')))
	if _, err := ReadBrokerFrame(reader); !errors.Is(err, ErrBrokerFrameTooLarge) {
		t.Fatalf("ReadBrokerFrame() error = %v, want ErrBrokerFrameTooLarge", err)
	}

	reader = NewBrokerFrameReader(bytes.NewReader([]byte("\r\n")))
	if _, err := ReadBrokerFrame(reader); !errors.Is(err, ErrBrokerMalformedRequest) {
		t.Fatalf("ReadBrokerFrame(empty) error = %v, want ErrBrokerMalformedRequest", err)
	}
}

func TestMarshalBrokerWireResponse(t *testing.T) {
	data, err := MarshalBrokerWireResponse(BrokerWireResponse{
		RequestID: "request-1",
		Authorization: &BrokerAuthorization{
			Operation:       BrokerStart,
			UserID:          "user-1000",
			ConfigurationID: "configuration-1",
			SnapshotID:      "snapshot-1",
		},
	})
	if err != nil {
		t.Fatalf("MarshalBrokerWireResponse() error = %v", err)
	}
	if !bytes.HasSuffix(data, []byte{'\n'}) || bytes.Contains(data, []byte("/tmp")) {
		t.Fatalf("MarshalBrokerWireResponse() = %q", data)
	}

	_, err = MarshalBrokerWireResponse(BrokerWireResponse{RequestID: "request-1"})
	if !errors.Is(err, ErrBrokerMalformedRequest) {
		t.Fatalf("MarshalBrokerWireResponse(no result) error = %v, want ErrBrokerMalformedRequest", err)
	}
}
