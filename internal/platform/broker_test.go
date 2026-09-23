package platform

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestBrokerAuthorizerConsumesSameUserSnapshotGrant(t *testing.T) {
	now := time.Date(2026, time.September, 23, 20, 0, 0, 0, time.UTC)
	authorizer := NewBrokerAuthorizer(func() time.Time { return now })
	grant := SnapshotGrant{
		UserID:          "user-1000",
		ConfigurationID: "configuration-1",
		SnapshotID:      "snapshot-1",
		ExpiresAt:       now.Add(time.Minute),
	}
	if err := authorizer.RegisterGrant(grant); err != nil {
		t.Fatalf("RegisterGrant() error = %v", err)
	}

	started, err := authorizer.Authorize(BrokerRequest{
		Version:         PrivilegedBrokerProtocolVersion,
		Operation:       BrokerStart,
		RequestID:       "request-1",
		UserID:          grant.UserID,
		ConfigurationID: grant.ConfigurationID,
		SnapshotID:      grant.SnapshotID,
	})
	if err != nil {
		t.Fatalf("Authorize(start) error = %v", err)
	}
	if started.Operation != BrokerStart || started.SnapshotID != grant.SnapshotID {
		t.Fatalf("Authorize(start) = %#v, want start authorization for %q", started, grant.SnapshotID)
	}

	_, err = authorizer.Authorize(BrokerRequest{
		Version:         PrivilegedBrokerProtocolVersion,
		Operation:       BrokerStart,
		RequestID:       "request-2",
		UserID:          grant.UserID,
		ConfigurationID: "configuration-2",
		SnapshotID:      grant.SnapshotID,
	})
	if !errors.Is(err, ErrBrokerNotFound) {
		t.Fatalf("Authorize(reused grant) error = %v, want ErrBrokerNotFound", err)
	}
}

func TestBrokerAuthorizerRejectsCrossUserExpiredAndMismatchedGrants(t *testing.T) {
	now := time.Date(2026, time.September, 23, 20, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		grant   SnapshotGrant
		request BrokerRequest
		wantErr error
	}{
		{
			name: "cross user",
			grant: SnapshotGrant{UserID: "user-1000", ConfigurationID: "configuration-1", SnapshotID: "snapshot-1", ExpiresAt: now.Add(time.Minute)},
			request: BrokerRequest{Version: PrivilegedBrokerProtocolVersion, Operation: BrokerStart, RequestID: "request-1", UserID: "user-1001", ConfigurationID: "configuration-1", SnapshotID: "snapshot-1"},
			wantErr: ErrBrokerNotFound,
		},
		{
			name: "configuration mismatch",
			grant: SnapshotGrant{UserID: "user-1000", ConfigurationID: "configuration-1", SnapshotID: "snapshot-1", ExpiresAt: now.Add(time.Minute)},
			request: BrokerRequest{Version: PrivilegedBrokerProtocolVersion, Operation: BrokerStart, RequestID: "request-1", UserID: "user-1000", ConfigurationID: "configuration-2", SnapshotID: "snapshot-1"},
			wantErr: ErrBrokerUnauthorized,
		},
		{
			name: "expired",
			grant: SnapshotGrant{UserID: "user-1000", ConfigurationID: "configuration-1", SnapshotID: "snapshot-1", ExpiresAt: now.Add(time.Second)},
			request: BrokerRequest{Version: PrivilegedBrokerProtocolVersion, Operation: BrokerStart, RequestID: "request-1", UserID: "user-1000", ConfigurationID: "configuration-1", SnapshotID: "snapshot-1"},
			wantErr: ErrBrokerExpired,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clock := now
			authorizer := NewBrokerAuthorizer(func() time.Time { return clock })
			if err := authorizer.RegisterGrant(test.grant); err != nil {
				t.Fatalf("RegisterGrant() error = %v", err)
			}
			if test.name == "expired" {
				clock = clock.Add(2 * time.Second)
			}
			_, err := authorizer.Authorize(test.request)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Authorize() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestBrokerAuthorizerStopRequiresOwnerAndReleasesConfiguration(t *testing.T) {
	now := time.Date(2026, time.September, 23, 20, 0, 0, 0, time.UTC)
	authorizer := NewBrokerAuthorizer(func() time.Time { return now })
	grant := SnapshotGrant{UserID: "user-1000", ConfigurationID: "configuration-1", SnapshotID: "snapshot-1", ExpiresAt: now.Add(time.Minute)}
	if err := authorizer.RegisterGrant(grant); err != nil {
		t.Fatalf("RegisterGrant() error = %v", err)
	}
	start := BrokerRequest{Version: PrivilegedBrokerProtocolVersion, Operation: BrokerStart, RequestID: "request-1", UserID: grant.UserID, ConfigurationID: grant.ConfigurationID, SnapshotID: grant.SnapshotID}
	if _, err := authorizer.Authorize(start); err != nil {
		t.Fatalf("Authorize(start) error = %v", err)
	}

	_, err := authorizer.Authorize(BrokerRequest{Version: PrivilegedBrokerProtocolVersion, Operation: BrokerStop, RequestID: "request-2", UserID: "user-1001", ConfigurationID: grant.ConfigurationID})
	if !errors.Is(err, ErrBrokerNotFound) {
		t.Fatalf("Authorize(cross-user stop) error = %v, want ErrBrokerNotFound", err)
	}
	stopped, err := authorizer.Authorize(BrokerRequest{Version: PrivilegedBrokerProtocolVersion, Operation: BrokerCancel, RequestID: "request-3", UserID: grant.UserID, ConfigurationID: grant.ConfigurationID})
	if err != nil {
		t.Fatalf("Authorize(cancel) error = %v", err)
	}
	if stopped.Operation != BrokerCancel || stopped.SnapshotID != grant.SnapshotID {
		t.Fatalf("Authorize(cancel) = %#v, want cancel authorization for %q", stopped, grant.SnapshotID)
	}
}

func TestBrokerAuthorizerRetainsBoundedPrivateAuditTrail(t *testing.T) {
	now := time.Date(2026, time.September, 23, 20, 0, 0, 0, time.UTC)
	authorizer := NewBrokerAuthorizer(func() time.Time { return now })
	for index := 0; index < maxBrokerAuditEntries+1; index++ {
		_, err := authorizer.Authorize(BrokerRequest{
			Version:         PrivilegedBrokerProtocolVersion,
			Operation:       BrokerStart,
			RequestID:       fmt.Sprintf("request-%d", index),
			UserID:          "user-1000",
			ConfigurationID: "configuration-1",
			SnapshotID:      "snapshot-1",
		})
		if !errors.Is(err, ErrBrokerNotFound) {
			t.Fatalf("Authorize() error = %v, want ErrBrokerNotFound", err)
		}
	}
	audit := authorizer.AuditTrail()
	if len(audit) != maxBrokerAuditEntries || audit[0].RequestID != "request-1" || audit[len(audit)-1].RequestID != fmt.Sprintf("request-%d", maxBrokerAuditEntries) {
		t.Fatalf("AuditTrail() = %#v, want bounded oldest-first records", audit)
	}
	if audit[0].Outcome != BrokerAuditRejected || audit[0].Code != "not_found" || audit[0].SnapshotID != "snapshot-1" {
		t.Fatalf("AuditTrail() first record = %#v", audit[0])
	}
	audit[0].RequestID = "mutated"
	if authorizer.AuditTrail()[0].RequestID == "mutated" {
		t.Fatal("AuditTrail() returned mutable broker state")
	}
}

func TestBrokerAuthorizerRejectsUnsafeRequestForms(t *testing.T) {
	authorizer := NewBrokerAuthorizer(time.Now)
	if err := authorizer.RegisterGrant(SnapshotGrant{UserID: "user-1000", ConfigurationID: "configuration-1", SnapshotID: "../snapshot", ExpiresAt: time.Now().Add(time.Minute)}); !errors.Is(err, ErrBrokerMalformedRequest) {
		t.Fatalf("RegisterGrant(unsafe snapshot ID) error = %v, want ErrBrokerMalformedRequest", err)
	}
	_, err := authorizer.Authorize(BrokerRequest{
		Version:         PrivilegedBrokerProtocolVersion,
		Operation:       BrokerStop,
		RequestID:       "request-1",
		UserID:          "user-1000",
		ConfigurationID: "configuration-1",
		SnapshotID:      "snapshot-1",
	})
	if !errors.Is(err, ErrBrokerMalformedRequest) {
		t.Fatalf("Authorize(stop with snapshot) error = %v, want ErrBrokerMalformedRequest", err)
	}
}
