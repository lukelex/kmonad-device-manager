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
