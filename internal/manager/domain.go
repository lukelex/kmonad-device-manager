package manager

import "time"

// The types in this file are the versioned manager-domain vocabulary used by
// the local API. They intentionally contain no platform locators, file paths,
// or process identifiers.

type ResourceKind string

const (
	ResourceManager       ResourceKind = "manager"
	ResourceDevice        ResourceKind = "device"
	ResourceConfiguration ResourceKind = "configuration"
	ResourceOperation     ResourceKind = "operation"
	ResourceDiagnostic    ResourceKind = "diagnostic"
)

type ResourceRef struct {
	Kind ResourceKind `json:"kind"`
	ID   string       `json:"id"`
}

type DeviceAvailability string

const (
	DeviceConnected    DeviceAvailability = "connected"
	DeviceDisconnected DeviceAvailability = "disconnected"
	DeviceInaccessible DeviceAvailability = "inaccessible"
	DeviceUnsupported  DeviceAvailability = "unsupported"
	DeviceConflicting  DeviceAvailability = "conflicting"
)

type IdentityStability string

const (
	IdentitySerial   IdentityStability = "serial"
	IdentityTopology IdentityStability = "topology"
	IdentityPlatform IdentityStability = "platform"
	IdentityUnknown  IdentityStability = "unknown"
)

type Device struct {
	ID                string             `json:"id"`
	DisplayName       string             `json:"display_name"`
	Vendor            string             `json:"vendor,omitempty"`
	Product           string             `json:"product,omitempty"`
	Serial            string             `json:"serial,omitempty"`
	Availability      DeviceAvailability `json:"availability"`
	IdentityStability IdentityStability  `json:"identity_stability"`
	ConfiguredBy      []string           `json:"configured_by"`
	RuntimeConflict   bool               `json:"runtime_conflict"`
	ReasonCode        ReasonCode         `json:"reason_code"`
	Reason            string             `json:"reason"`
}

type ConfigurationOwnership string

const (
	ConfigurationManaged  ConfigurationOwnership = "managed"
	ConfigurationExternal ConfigurationOwnership = "external"
)

type RuntimePhase string

const (
	RuntimeDiscovered RuntimePhase = "discovered"
	RuntimeValidating RuntimePhase = "validating"
	RuntimeWaiting    RuntimePhase = "waiting"
	RuntimeApplying   RuntimePhase = "applying"
	RuntimeRunning    RuntimePhase = "running"
	RuntimeBackoff    RuntimePhase = "backoff"
	RuntimeFailed     RuntimePhase = "failed"
	RuntimeDuplicate  RuntimePhase = "duplicate"
	RuntimeStopped    RuntimePhase = "stopped"
	RuntimeDisabled   RuntimePhase = "disabled"
	RuntimeRecovering RuntimePhase = "recovering"
)

type RuntimeState struct {
	Phase        RuntimePhase `json:"phase"`
	ReasonCode   ReasonCode   `json:"reason_code"`
	Reason       string       `json:"reason"`
	Connected    bool         `json:"connected"`
	Healthy      bool         `json:"healthy"`
	RetryAt      *time.Time   `json:"retry_at,omitempty"`
	FailureCount int          `json:"failure_count"`
}

type Configuration struct {
	ID              string                 `json:"id"`
	Name            string                 `json:"name"`
	Ownership       ConfigurationOwnership `json:"ownership"`
	Enabled         bool                   `json:"enabled"`
	DeviceID        string                 `json:"device_id"`
	DesiredRevision uint64                 `json:"desired_revision"`
	ActiveRevision  uint64                 `json:"active_revision"`
	Runtime         RuntimeState           `json:"runtime"`
	LastOperation   *Operation             `json:"last_operation,omitempty"`
}

// ManagerHealth summarizes the service's public operational health without
// disclosing host process IDs, filesystem locations, or platform handles.
type ManagerHealth struct {
	Healthy             bool       `json:"healthy"`
	ReasonCode          ReasonCode `json:"reason_code"`
	Reason              string     `json:"reason"`
	LastProgressAt      *time.Time `json:"last_progress_at,omitempty"`
	ReconcileCount      uint64     `json:"reconcile_count"`
	FailureCount        uint64     `json:"failure_count"`
	MetricsAvailable    bool       `json:"metrics_available"`
	StatusWriteFailures uint64     `json:"status_write_failures"`
}

// Snapshot is the point-in-time public state owned by the reconciliation
// goroutine. StateRevision increases monotonically as snapshots/reconciliation
// progress, allowing clients to order refresh results.
type Snapshot struct {
	StateRevision  uint64          `json:"state_revision"`
	Devices        []Device        `json:"devices"`
	Configurations []Configuration `json:"configurations"`
	Operations     []Operation     `json:"operations"`
	Health         ManagerHealth   `json:"health"`
}

// ManagedConfigurationModel is a platform-neutral candidate for a future
// manager-owned configuration. Behavior must not contain a KMonad defcfg or
// input target: the manager resolves DeviceID and renders that target itself.
type ManagedConfigurationModel struct {
	DeviceID string `json:"device_id"`
	Behavior string `json:"behavior"`
}

type ValidationOutcome string

const (
	ValidationValid    ValidationOutcome = "valid"
	ValidationRejected ValidationOutcome = "rejected"
	ValidationBlocked  ValidationOutcome = "blocked"
)

type ValidationResult struct {
	Outcome     ValidationOutcome `json:"outcome"`
	ReasonCode  ReasonCode        `json:"reason_code"`
	Reason      string            `json:"reason"`
	Diagnostics []Diagnostic      `json:"diagnostics"`
}

type DiagnosticSeverity string

const (
	DiagnosticOK        DiagnosticSeverity = "ok"
	DiagnosticWarning   DiagnosticSeverity = "warning"
	DiagnosticTemporary DiagnosticSeverity = "temporary"
	DiagnosticError     DiagnosticSeverity = "error"
)

type Diagnostic struct {
	ID          string             `json:"id"`
	Severity    DiagnosticSeverity `json:"severity"`
	ReasonCode  ReasonCode         `json:"reason_code"`
	Summary     string             `json:"summary"`
	Remediation string             `json:"remediation"`
	Resource    *ResourceRef       `json:"resource,omitempty"`
}

type CapabilityName string

const (
	CapabilityDeviceDiscovery               CapabilityName = "device_discovery"
	CapabilityDeviceIdentification          CapabilityName = "device_identification"
	CapabilityCandidateValidation           CapabilityName = "candidate_validation"
	CapabilityManagedConfigurations         CapabilityName = "managed_configurations"
	CapabilityExternalConfigurationAdoption CapabilityName = "external_configuration_adoption"
	CapabilityEventStream                   CapabilityName = "event_stream"
	CapabilityMultipleIndependentKeyboards  CapabilityName = "multiple_independent_keyboards"
	CapabilityAutomaticHotplugRecovery      CapabilityName = "automatic_hotplug_recovery"
)

type Capability struct {
	Name       CapabilityName `json:"name"`
	Available  bool           `json:"available"`
	ReasonCode ReasonCode     `json:"reason_code"`
	Reason     string         `json:"reason"`
}

type OperationKind string

const (
	OperationIdentify  OperationKind = "identify"
	OperationValidate  OperationKind = "validate"
	OperationApply     OperationKind = "apply"
	OperationRollback  OperationKind = "rollback"
	OperationAdopt     OperationKind = "adopt"
	OperationLifecycle OperationKind = "lifecycle"
)

type OperationState string

const (
	OperationQueued     OperationState = "queued"
	OperationRunning    OperationState = "running"
	OperationWaiting    OperationState = "waiting"
	OperationCancelling OperationState = "cancelling"
	OperationSucceeded  OperationState = "succeeded"
	OperationRejected   OperationState = "rejected"
	OperationFailed     OperationState = "failed"
	OperationRolledBack OperationState = "rolled_back"
	OperationCancelled  OperationState = "cancelled"
)

type Operation struct {
	ID                    string            `json:"id"`
	Kind                  OperationKind     `json:"kind"`
	State                 OperationState    `json:"state"`
	Resource              ResourceRef       `json:"resource"`
	StartedAt             time.Time         `json:"started_at"`
	UpdatedAt             time.Time         `json:"updated_at"`
	ReasonCode            ReasonCode        `json:"reason_code"`
	Reason                string            `json:"reason"`
	Validation            *ValidationResult `json:"validation,omitempty"`
	ConfigurationRevision uint64            `json:"configuration_revision,omitempty"`
}

type EventType string

const (
	EventDeviceAdded                 EventType = "device.added"
	EventDeviceAvailabilityChanged   EventType = "device.availability_changed"
	EventConfigurationChanged        EventType = "configuration.changed"
	EventConfigurationRuntimeChanged EventType = "configuration.runtime_changed"
	EventConfigurationRejected       EventType = "configuration.rejected"
	EventConfigurationRolledBack     EventType = "configuration.rolled_back"
	EventOperationChanged            EventType = "operation.changed"
	EventDiagnosticChanged           EventType = "diagnostic.changed"
	EventManagerResyncRequired       EventType = "manager.resync_required"
)

type Event struct {
	EventID       uint64         `json:"event_id"`
	StateRevision uint64         `json:"state_revision"`
	Time          time.Time      `json:"time"`
	Type          EventType      `json:"event_type"`
	Resource      ResourceRef    `json:"resource"`
	ReasonCode    ReasonCode     `json:"reason_code"`
	Data          map[string]any `json:"data"`
}

// ReasonCode is a stable, machine-readable explanation for a resource state,
// operation outcome, or diagnostic. Its display text is deliberately separate.
type ReasonCode string

const (
	ReasonDeviceConnected         ReasonCode = "device_connected"
	ReasonDeviceDisconnected      ReasonCode = "device_disconnected"
	ReasonDeviceInaccessible      ReasonCode = "device_inaccessible"
	ReasonDeviceUnsupported       ReasonCode = "device_unsupported"
	ReasonDeviceConflicting       ReasonCode = "device_conflicting"
	ReasonDeviceIdentityAmbiguous ReasonCode = "device_identity_ambiguous"

	ReasonConfigurationDiscovered       ReasonCode = "configuration_discovered"
	ReasonConfigurationDisabled         ReasonCode = "configuration_disabled"
	ReasonConfigurationExternalReadOnly ReasonCode = "configuration_external_read_only"
	ReasonConfigurationAdoptionRequired ReasonCode = "configuration_adoption_required"
	ReasonConfigurationRevisionStale    ReasonCode = "configuration_revision_stale"
	ReasonConfigurationLimitReached     ReasonCode = "configuration_limit_reached"
	ReasonConfigurationChanged          ReasonCode = "configuration_changed"
	ReasonConfigurationTooLarge         ReasonCode = "configuration_too_large"

	ReasonValidationSucceeded  ReasonCode = "validation_succeeded"
	ReasonValidationFailed     ReasonCode = "validation_failed"
	ReasonValidationTimedOut   ReasonCode = "validation_timed_out"
	ReasonValidationBlocked    ReasonCode = "validation_blocked"
	ReasonCandidateUnsupported ReasonCode = "candidate_unsupported"

	ReasonRuntimeStarting              ReasonCode = "runtime_starting"
	ReasonRuntimeRunning               ReasonCode = "runtime_running"
	ReasonRuntimeWaitingForDevice      ReasonCode = "runtime_waiting_for_device"
	ReasonRuntimeBackoff               ReasonCode = "runtime_backoff"
	ReasonRuntimeProcessExited         ReasonCode = "runtime_process_exited"
	ReasonRuntimeWatchdogTimeout       ReasonCode = "runtime_watchdog_timeout"
	ReasonRuntimeProcessUnhealthy      ReasonCode = "runtime_process_unhealthy"
	ReasonRuntimeOwnershipLost         ReasonCode = "runtime_ownership_lost"
	ReasonRuntimeDuplicateDevice       ReasonCode = "runtime_duplicate_device"
	ReasonRuntimePendingUpdateRejected ReasonCode = "runtime_pending_update_rejected"
	ReasonRuntimeActivationFailed      ReasonCode = "runtime_activation_failed"
	ReasonRuntimeRollbackSucceeded     ReasonCode = "runtime_rollback_succeeded"
	ReasonRuntimeRollbackFailed        ReasonCode = "runtime_rollback_failed"
	ReasonRuntimeStopped               ReasonCode = "runtime_stopped"

	ReasonOperationQueued      ReasonCode = "operation_queued"
	ReasonOperationRunning     ReasonCode = "operation_running"
	ReasonOperationSucceeded   ReasonCode = "operation_succeeded"
	ReasonOperationCancelled   ReasonCode = "operation_cancelled"
	ReasonOperationTimedOut    ReasonCode = "operation_timed_out"
	ReasonOperationUnsupported ReasonCode = "operation_unsupported"
	ReasonCapabilityAvailable  ReasonCode = "capability_available"
	ReasonManagerHealthy       ReasonCode = "manager_healthy"
	ReasonManagerStarting      ReasonCode = "manager_starting"

	ReasonDependencyUnavailable ReasonCode = "dependency_unavailable"
	ReasonPermissionDenied      ReasonCode = "permission_denied"
	ReasonInternal              ReasonCode = "internal"
)
