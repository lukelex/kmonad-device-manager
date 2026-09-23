package platform

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
	"time"
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

func TestHandleBrokerWireRequestReturnsBoundedAuthorization(t *testing.T) {
	now := time.Date(2026, time.September, 24, 12, 0, 0, 0, time.UTC)
	authorizer := NewBrokerAuthorizer(func() time.Time { return now })
	if err := authorizer.RegisterGrant(SnapshotGrant{
		UserID:          "user-1000",
		ConfigurationID: "configuration-1",
		SnapshotID:      "snapshot-1",
		ExpiresAt:       now.Add(time.Minute),
	}); err != nil {
		t.Fatalf("RegisterGrant() error = %v", err)
	}

	data, err := HandleBrokerWireRequest([]byte(`{"version":1,"operation":"start","request_id":"request-1","configuration_id":"configuration-1","snapshot_id":"snapshot-1"}`), "user-1000", authorizer)
	if err != nil {
		t.Fatalf("HandleBrokerWireRequest() error = %v", err)
	}
	var response BrokerWireResponse
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if response.Error != nil || response.Authorization == nil || response.Authorization.UserID != "user-1000" || response.Authorization.Operation != BrokerStart {
		t.Fatalf("HandleBrokerWireRequest() response = %#v", response)
	}
}

func TestHandleBrokerWireRequestReturnsSafeAuthorizationErrors(t *testing.T) {
	authorizer := NewBrokerAuthorizer(time.Now)
	data, err := HandleBrokerWireRequest([]byte(`{"version":1,"operation":"start","request_id":"request-1","configuration_id":"configuration-1","snapshot_id":"snapshot-1"}`), "user-1000", authorizer)
	if err != nil {
		t.Fatalf("HandleBrokerWireRequest() error = %v", err)
	}
	var response BrokerWireResponse
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if response.Authorization != nil || response.Error == nil || response.Error.Code != "snapshot_unavailable" || bytes.Contains(data, []byte("/")) {
		t.Fatalf("HandleBrokerWireRequest() response = %s", data)
	}

	_, err = HandleBrokerWireRequest([]byte(`{"version":1,"operation":"start","request_id":"request-1","configuration_id":"configuration-1","snapshot_id":"snapshot-1","command":"/bin/sh"}`), "user-1000", authorizer)
	if !errors.Is(err, ErrBrokerMalformedRequest) {
		t.Fatalf("HandleBrokerWireRequest(unknown field) error = %v, want ErrBrokerMalformedRequest", err)
	}
}
