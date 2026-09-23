package platform

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var (
	ErrBrokerSnapshotStore    = errors.New("privileged broker snapshot store is invalid")
	ErrBrokerSnapshotTooLarge = errors.New("privileged broker snapshot exceeds the maximum size")
	ErrBrokerSnapshotExists   = errors.New("privileged broker snapshot already exists")
	ErrBrokerSnapshotMissing  = errors.New("privileged broker snapshot is unavailable")
)

// BrokerSnapshotStore is the broker-private storage for copied, validated
// configuration snapshots. Its Stage method accepts a reader supplied by the
// broker's trusted platform-specific verifier, not controller JSON or a path
// received over the controller-to-broker protocol.
type BrokerSnapshotStore struct {
	root     string
	maxBytes int64
	now      func() time.Time
}

// BrokerStoredSnapshot is an opaque handle to one broker-private copy. The
// storage path is intentionally unexported so it cannot become a controller
// protocol value.
type BrokerStoredSnapshot struct {
	grant  SnapshotGrant
	digest string
	size   int64
	path   string
}

func (snapshot BrokerStoredSnapshot) Grant() SnapshotGrant { return snapshot.grant }
func (snapshot BrokerStoredSnapshot) Digest() string       { return snapshot.digest }
func (snapshot BrokerStoredSnapshot) Size() int64          { return snapshot.size }

// NewBrokerSnapshotStore creates a private snapshot store. The store root must
// not be accessible to group or other users. A nil clock selects time.Now.
func NewBrokerSnapshotStore(root string, maxBytes int64, now func() time.Time) (*BrokerSnapshotStore, error) {
	if root == "" || maxBytes <= 0 {
		return nil, fmt.Errorf("%w: root and positive maximum size are required", ErrBrokerSnapshotStore)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve root: %v", ErrBrokerSnapshotStore, err)
	}
	if err := os.MkdirAll(absRoot, 0o700); err != nil {
		return nil, fmt.Errorf("%w: create root: %v", ErrBrokerSnapshotStore, err)
	}
	if err := ensurePrivateBrokerDirectory(absRoot); err != nil {
		return nil, err
	}
	if now == nil {
		now = time.Now
	}
	return &BrokerSnapshotStore{root: absRoot, maxBytes: maxBytes, now: now}, nil
}

// Stage copies one already-verified snapshot reader into broker-private,
// owner-only storage. It preserves the one-time snapshot ID selected by the
// broker and produces a digest for later integrity/audit checks.
func (store *BrokerSnapshotStore) Stage(grant SnapshotGrant, source io.Reader) (BrokerStoredSnapshot, error) {
	if store == nil || source == nil {
		return BrokerStoredSnapshot{}, fmt.Errorf("%w: store and source are required", ErrBrokerSnapshotStore)
	}
	if err := validateGrant(grant, store.now()); err != nil {
		return BrokerStoredSnapshot{}, err
	}
	directory := filepath.Join(store.root, grant.UserID, grant.ConfigurationID)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return BrokerStoredSnapshot{}, fmt.Errorf("%w: create snapshot directory: %v", ErrBrokerSnapshotStore, err)
	}
	if err := ensurePrivateBrokerDirectory(directory); err != nil {
		return BrokerStoredSnapshot{}, err
	}
	path := filepath.Join(directory, grant.SnapshotID+".kbd")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return BrokerStoredSnapshot{}, fmt.Errorf("%w: snapshot %q", ErrBrokerSnapshotExists, grant.SnapshotID)
		}
		return BrokerStoredSnapshot{}, fmt.Errorf("%w: create snapshot: %v", ErrBrokerSnapshotStore, err)
	}
	completed := false
	defer func() {
		_ = file.Close()
		if !completed {
			_ = os.Remove(path)
		}
	}()

	digest := sha256.New()
	size, err := io.Copy(io.MultiWriter(file, digest), io.LimitReader(source, store.maxBytes+1))
	if err != nil {
		return BrokerStoredSnapshot{}, fmt.Errorf("%w: copy snapshot: %v", ErrBrokerSnapshotStore, err)
	}
	if size > store.maxBytes {
		return BrokerStoredSnapshot{}, ErrBrokerSnapshotTooLarge
	}
	if err := file.Chmod(0o400); err != nil {
		return BrokerStoredSnapshot{}, fmt.Errorf("%w: make snapshot immutable: %v", ErrBrokerSnapshotStore, err)
	}
	if err := file.Sync(); err != nil {
		return BrokerStoredSnapshot{}, fmt.Errorf("%w: sync snapshot: %v", ErrBrokerSnapshotStore, err)
	}
	if err := file.Close(); err != nil {
		return BrokerStoredSnapshot{}, fmt.Errorf("%w: close snapshot: %v", ErrBrokerSnapshotStore, err)
	}
	completed = true
	return BrokerStoredSnapshot{
		grant:  grant,
		digest: hex.EncodeToString(digest.Sum(nil)),
		size:   size,
		path:   path,
	}, nil
}

// Open returns a read-only handle to the private staged copy. It verifies that
// the handle belongs to this store before opening it.
func (store *BrokerSnapshotStore) Open(snapshot BrokerStoredSnapshot) (io.ReadCloser, error) {
	if store == nil || !pathWithinBrokerRoot(store.root, snapshot.path) {
		return nil, ErrBrokerSnapshotMissing
	}
	file, err := os.Open(snapshot.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrBrokerSnapshotMissing
		}
		return nil, fmt.Errorf("%w: open snapshot: %v", ErrBrokerSnapshotStore, err)
	}
	return file, nil
}

func ensurePrivateBrokerDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("%w: stat directory: %v", ErrBrokerSnapshotStore, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%w: directory %q must be owner-only", ErrBrokerSnapshotStore, path)
	}
	return nil
}

func pathWithinBrokerRoot(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." || filepath.IsAbs(relative) {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
