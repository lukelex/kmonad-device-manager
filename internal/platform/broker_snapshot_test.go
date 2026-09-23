package platform

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBrokerSnapshotStoreStagesPrivateBoundedCopy(t *testing.T) {
	now := time.Date(2026, time.September, 24, 12, 0, 0, 0, time.UTC)
	store, err := NewBrokerSnapshotStore(privateBrokerTestDirectory(t), 128, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewBrokerSnapshotStore() error = %v", err)
	}
	content := "(defsrc caps)\n(deflayer base esc)\n"
	grant := SnapshotGrant{UserID: "user-1000", ConfigurationID: "configuration-1", SnapshotID: "snapshot-1", ExpiresAt: now.Add(time.Minute)}
	snapshot, err := store.Stage(grant, strings.NewReader(content))
	if err != nil {
		t.Fatalf("Stage() error = %v", err)
	}
	wantDigest := sha256.Sum256([]byte(content))
	if snapshot.Grant() != grant || snapshot.Size() != int64(len(content)) || snapshot.Digest() != hex.EncodeToString(wantDigest[:]) {
		t.Fatalf("Stage() = %#v, want grant, size, and digest for staged content", snapshot)
	}
	info, err := os.Stat(snapshot.path)
	if err != nil {
		t.Fatalf("stat staged snapshot: %v", err)
	}
	if info.Mode().Perm() != 0o400 {
		t.Fatalf("staged snapshot mode = %o, want 0400", info.Mode().Perm())
	}
	reader, err := store.Open(snapshot)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer reader.Close()
	got, err := io.ReadAll(reader)
	if err != nil || string(got) != content {
		t.Fatalf("Open() content = %q, %v; want %q", got, err, content)
	}
}

func TestBrokerSnapshotStoreRejectsUnsafeStaging(t *testing.T) {
	now := time.Date(2026, time.September, 24, 12, 0, 0, 0, time.UTC)
	store, err := NewBrokerSnapshotStore(privateBrokerTestDirectory(t), 3, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewBrokerSnapshotStore() error = %v", err)
	}
	grant := SnapshotGrant{UserID: "user-1000", ConfigurationID: "configuration-1", SnapshotID: "snapshot-1", ExpiresAt: now.Add(time.Minute)}
	if _, err := store.Stage(grant, strings.NewReader("four")); !errors.Is(err, ErrBrokerSnapshotTooLarge) {
		t.Fatalf("Stage(oversized) error = %v, want ErrBrokerSnapshotTooLarge", err)
	}
	if _, err := os.Stat(filepath.Join(store.root, grant.UserID, grant.ConfigurationID, grant.SnapshotID+".kbd")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("oversized snapshot remained in the store: %v", err)
	}
	if _, err := store.Stage(grant, strings.NewReader("one")); err != nil {
		t.Fatalf("Stage() error = %v", err)
	}
	if _, err := store.Stage(grant, strings.NewReader("two")); !errors.Is(err, ErrBrokerSnapshotExists) {
		t.Fatalf("Stage(duplicate) error = %v, want ErrBrokerSnapshotExists", err)
	}
	_, err = store.Stage(SnapshotGrant{UserID: "user-1000", ConfigurationID: "configuration-1", SnapshotID: "snapshot-2", ExpiresAt: now}, strings.NewReader("one"))
	if !errors.Is(err, ErrBrokerExpired) {
		t.Fatalf("Stage(expired) error = %v, want ErrBrokerExpired", err)
	}
}

func TestBrokerSnapshotStoreRequiresPrivateRootAndOwnsPaths(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := NewBrokerSnapshotStore(root, 1, time.Now); !errors.Is(err, ErrBrokerSnapshotStore) {
		t.Fatalf("NewBrokerSnapshotStore(public root) error = %v, want ErrBrokerSnapshotStore", err)
	}

	store, err := NewBrokerSnapshotStore(privateBrokerTestDirectory(t), 1, time.Now)
	if err != nil {
		t.Fatalf("NewBrokerSnapshotStore() error = %v", err)
	}
	_, err = store.Open(BrokerStoredSnapshot{path: filepath.Join(t.TempDir(), "outside.kbd")})
	if !errors.Is(err, ErrBrokerSnapshotMissing) {
		t.Fatalf("Open(outside snapshot) error = %v, want ErrBrokerSnapshotMissing", err)
	}
}

func privateBrokerTestDirectory(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	return directory
}

func TestBrokerSnapshotStoreOpensOnlyAuthorizedIntactSnapshot(t *testing.T) {
	now := time.Date(2026, time.September, 24, 12, 0, 0, 0, time.UTC)
	store, err := NewBrokerSnapshotStore(privateBrokerTestDirectory(t), 128, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewBrokerSnapshotStore() error = %v", err)
	}
	grant := SnapshotGrant{UserID: "user-1000", ConfigurationID: "configuration-1", SnapshotID: "snapshot-1", ExpiresAt: now.Add(time.Minute)}
	staged, err := store.Stage(grant, strings.NewReader("snapshot content"))
	if err != nil {
		t.Fatalf("Stage() error = %v", err)
	}
	authorization := BrokerAuthorization{Operation: BrokerStart, UserID: grant.UserID, ConfigurationID: grant.ConfigurationID, SnapshotID: grant.SnapshotID}
	opened, reader, err := store.OpenAuthorization(authorization)
	if err != nil {
		t.Fatalf("OpenAuthorization() error = %v", err)
	}
	data, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || string(data) != "snapshot content" || opened.Digest() != staged.Digest() {
		t.Fatalf("OpenAuthorization() = %#v, %q, %v, %v", opened, data, readErr, closeErr)
	}

	if err := os.Chmod(staged.path, 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err = store.OpenAuthorization(authorization)
	if !errors.Is(err, ErrBrokerSnapshotMissing) {
		t.Fatalf("OpenAuthorization(modified mode) error = %v, want ErrBrokerSnapshotMissing", err)
	}
	_, _, err = store.OpenAuthorization(BrokerAuthorization{Operation: BrokerStart, UserID: grant.UserID, ConfigurationID: grant.ConfigurationID, SnapshotID: "snapshot-2"})
	if !errors.Is(err, ErrBrokerSnapshotMissing) {
		t.Fatalf("OpenAuthorization(unrecognized snapshot) error = %v, want ErrBrokerSnapshotMissing", err)
	}
}

func TestPrepareBrokerLaunchAbortsFailedReservation(t *testing.T) {
	now := time.Date(2026, time.September, 24, 12, 0, 0, 0, time.UTC)
	authorizer := NewBrokerAuthorizer(func() time.Time { return now })
	store, err := NewBrokerSnapshotStore(privateBrokerTestDirectory(t), 128, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewBrokerSnapshotStore() error = %v", err)
	}
	grant := SnapshotGrant{UserID: "user-1000", ConfigurationID: "configuration-1", SnapshotID: "missing-snapshot", ExpiresAt: now.Add(time.Minute)}
	if err := authorizer.RegisterGrant(grant); err != nil {
		t.Fatalf("RegisterGrant() error = %v", err)
	}
	authorization, err := authorizer.Authorize(BrokerRequest{Version: PrivilegedBrokerProtocolVersion, Operation: BrokerStart, RequestID: "request-1", UserID: grant.UserID, ConfigurationID: grant.ConfigurationID, SnapshotID: grant.SnapshotID})
	if err != nil {
		t.Fatalf("Authorize() error = %v", err)
	}
	if _, err := PrepareBrokerLaunch(authorizer, store, "request-1", authorization); !errors.Is(err, ErrBrokerSnapshotMissing) {
		t.Fatalf("PrepareBrokerLaunch() error = %v, want ErrBrokerSnapshotMissing", err)
	}

	retry := SnapshotGrant{UserID: grant.UserID, ConfigurationID: grant.ConfigurationID, SnapshotID: "replacement-snapshot", ExpiresAt: now.Add(time.Minute)}
	if err := authorizer.RegisterGrant(retry); err != nil {
		t.Fatalf("RegisterGrant(retry) error = %v", err)
	}
	if _, err := authorizer.Authorize(BrokerRequest{Version: PrivilegedBrokerProtocolVersion, Operation: BrokerStart, RequestID: "request-2", UserID: retry.UserID, ConfigurationID: retry.ConfigurationID, SnapshotID: retry.SnapshotID}); err != nil {
		t.Fatalf("Authorize(retry) error = %v", err)
	}
	audit := authorizer.AuditTrail()
	if audit[len(audit)-2].Outcome != BrokerAuditAborted || audit[len(audit)-2].Code != "launch_aborted" {
		t.Fatalf("AuditTrail() = %#v, want launch-aborted record", audit)
	}
}
