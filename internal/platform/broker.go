package platform

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// PrivilegedBrokerProtocolVersion is the first version of the narrow
// controller-to-broker authorization contract. It is intentionally separate
// from the manager API: clients never communicate with the privileged broker.
const PrivilegedBrokerProtocolVersion = 1

var (
	ErrBrokerMalformedRequest = errors.New("malformed privileged broker request")
	ErrBrokerUnsupported      = errors.New("unsupported privileged broker request")
	ErrBrokerUnauthorized     = errors.New("unauthorized privileged broker request")
	ErrBrokerExpired          = errors.New("privileged broker snapshot grant expired")
	ErrBrokerNotFound         = errors.New("privileged broker snapshot grant not found")
	ErrBrokerAlreadyActive    = errors.New("privileged broker configuration already active")
)

// BrokerOperation is an action a same-user controller may ask its privileged
// broker to perform. Start deliberately accepts only an opaque snapshot grant;
// a request never carries a command line, a path, or configuration bytes.
type BrokerOperation string

const (
	BrokerStart BrokerOperation = "start"
	BrokerStop  BrokerOperation = "stop"
)

// BrokerRequest is the transport-neutral privileged-broker request. UserID is
// supplied by the authenticated transport peer, not trusted from client input.
// RequestID is an opaque correlation value for audit logging.
type BrokerRequest struct {
	Version         int             `json:"version"`
	Operation       BrokerOperation `json:"operation"`
	RequestID       string          `json:"request_id"`
	UserID          string          `json:"user_id"`
	ConfigurationID string          `json:"configuration_id"`
	SnapshotID      string          `json:"snapshot_id,omitempty"`
}

// SnapshotGrant is held by the broker after it has independently verified and
// copied a manager-owned immutable snapshot into its private store. Its opaque
// ID is the only snapshot reference accepted in a BrokerStart request.
type SnapshotGrant struct {
	UserID          string
	ConfigurationID string
	SnapshotID      string
	ExpiresAt       time.Time
}

// BrokerAuthorization is the broker-local result of an authorized request. It
// intentionally has no process identifier, command, path, or device locator.
type BrokerAuthorization struct {
	Operation       BrokerOperation
	UserID          string
	ConfigurationID string
	SnapshotID      string
}

// BrokerAuthorizer models the authorization state owned by a future privileged
// macOS LaunchDaemon. It is transport and OS independent so its security
// invariants can be tested without a macOS host. The real broker must verify
// filesystem ownership and immutability before calling RegisterGrant.
type BrokerAuthorizer struct {
	mu     sync.Mutex
	now    func() time.Time
	grants map[string]SnapshotGrant
	active map[string]string
}

// NewBrokerAuthorizer constructs a broker authorizer. The clock is injected
// for deterministic expiry tests; nil selects time.Now.
func NewBrokerAuthorizer(now func() time.Time) *BrokerAuthorizer {
	if now == nil {
		now = time.Now
	}
	return &BrokerAuthorizer{
		now:    now,
		grants: make(map[string]SnapshotGrant),
		active: make(map[string]string),
	}
}

// RegisterGrant records a broker-verified snapshot for one controller user and
// configuration. A grant is single-use and expires before it can authorize a
// stale manager snapshot.
func (a *BrokerAuthorizer) RegisterGrant(grant SnapshotGrant) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := validateGrant(grant, a.now()); err != nil {
		return err
	}
	key := grantKey(grant.UserID, grant.SnapshotID)
	if _, exists := a.grants[key]; exists {
		return fmt.Errorf("%w: duplicate snapshot grant", ErrBrokerMalformedRequest)
	}
	a.grants[key] = grant
	return nil
}

// Authorize consumes a one-time start grant or authorizes the owning user to
// stop its active configuration. Calls are safe for concurrent transports.
func (a *BrokerAuthorizer) Authorize(request BrokerRequest) (BrokerAuthorization, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := validateRequest(request); err != nil {
		return BrokerAuthorization{}, err
	}

	activeKey := configurationKey(request.UserID, request.ConfigurationID)
	switch request.Operation {
	case BrokerStart:
		if _, active := a.active[activeKey]; active {
			return BrokerAuthorization{}, fmt.Errorf("%w: configuration %q", ErrBrokerAlreadyActive, request.ConfigurationID)
		}
		grant, found := a.grants[grantKey(request.UserID, request.SnapshotID)]
		if !found {
			return BrokerAuthorization{}, fmt.Errorf("%w: snapshot %q", ErrBrokerNotFound, request.SnapshotID)
		}
		if !grant.ExpiresAt.After(a.now()) {
			delete(a.grants, grantKey(request.UserID, request.SnapshotID))
			return BrokerAuthorization{}, fmt.Errorf("%w: snapshot %q", ErrBrokerExpired, request.SnapshotID)
		}
		if grant.ConfigurationID != request.ConfigurationID {
			return BrokerAuthorization{}, fmt.Errorf("%w: snapshot is not granted for configuration %q", ErrBrokerUnauthorized, request.ConfigurationID)
		}
		delete(a.grants, grantKey(request.UserID, request.SnapshotID))
		a.active[activeKey] = request.SnapshotID
		return BrokerAuthorization{
			Operation:       BrokerStart,
			UserID:          request.UserID,
			ConfigurationID: request.ConfigurationID,
			SnapshotID:      request.SnapshotID,
		}, nil
	case BrokerStop:
		snapshotID, active := a.active[activeKey]
		if !active {
			return BrokerAuthorization{}, fmt.Errorf("%w: configuration %q", ErrBrokerNotFound, request.ConfigurationID)
		}
		delete(a.active, activeKey)
		return BrokerAuthorization{
			Operation:       BrokerStop,
			UserID:          request.UserID,
			ConfigurationID: request.ConfigurationID,
			SnapshotID:      snapshotID,
		}, nil
	default:
		return BrokerAuthorization{}, fmt.Errorf("%w: operation %q", ErrBrokerUnsupported, request.Operation)
	}
}

func validateGrant(grant SnapshotGrant, now time.Time) error {
	if !validBrokerID(grant.UserID) || !validBrokerID(grant.ConfigurationID) || !validBrokerID(grant.SnapshotID) {
		return fmt.Errorf("%w: grant identifiers must be bounded opaque IDs", ErrBrokerMalformedRequest)
	}
	if !grant.ExpiresAt.After(now) {
		return fmt.Errorf("%w: grant expiry must be in the future", ErrBrokerExpired)
	}
	return nil
}

func validateRequest(request BrokerRequest) error {
	if request.Version != PrivilegedBrokerProtocolVersion {
		return fmt.Errorf("%w: protocol version %d", ErrBrokerUnsupported, request.Version)
	}
	if !validBrokerID(request.RequestID) || !validBrokerID(request.UserID) || !validBrokerID(request.ConfigurationID) {
		return fmt.Errorf("%w: request identifiers must be bounded opaque IDs", ErrBrokerMalformedRequest)
	}
	switch request.Operation {
	case BrokerStart:
		if !validBrokerID(request.SnapshotID) {
			return fmt.Errorf("%w: start requires an opaque snapshot ID", ErrBrokerMalformedRequest)
		}
	case BrokerStop:
		if request.SnapshotID != "" {
			return fmt.Errorf("%w: stop must not select a snapshot", ErrBrokerMalformedRequest)
		}
	default:
		return fmt.Errorf("%w: operation %q", ErrBrokerUnsupported, request.Operation)
	}
	return nil
}

func validBrokerID(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '-' || character == '_' || character == '.' {
			continue
		}
		return false
	}
	return true
}

func grantKey(userID, snapshotID string) string {
	return userID + "\x00" + snapshotID
}

func configurationKey(userID, configurationID string) string {
	return userID + "\x00" + configurationID
}
