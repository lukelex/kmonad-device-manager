# Manager API v1

**Status:** Same-user Unix-socket transport and `session.hello` are implemented.
Resource methods are not implemented yet and return `unsupported_capability`.

This specifies the local control plane between desktop clients and
`kmonad-device-manager`. The GUI is a client: it does not access input devices,
write manager-owned configuration files, or manage KMonad processes.

## Scope and compatibility

- API v1 is additive. The systemd service remains the lifecycle owner, and the
  manager continues to supervise external `.kbd` files without a GUI.
- Breaking changes to this contract or its implementation are acceptable while
  the GUI integration is being developed. The manager must still preserve the
  established systemd service behavior for external `.kbd` supervision,
  validated snapshot launches, hotplug handling, per-keyboard isolation,
  known-good update safety, and recovery.
- The manager must build, start, reconcile, supervise, recover, and stop its
  configured KMonad processes when the GUI is absent, the API socket is
  disabled/unavailable, or every API client disconnects. API work may not make
  the service depend on a GUI process or a GUI-managed configuration.
- An API listener, client, request, subscriber, or operation failure may affect
  only that API interaction. It must not block reconciliation, stop an existing
  mapping, or disrupt an unrelated configured keyboard.
- API request goroutines never mutate manager configuration state directly.
  Requests that need manager state enter a bounded command mailbox processed by
  the reconciliation owner. A full mailbox returns `resource_exhausted`; an
  expired request returns `deadline_exceeded`. The owner skips queued expired
  commands, and every command implementation must honor its context before
  performing a late mutation.
- The protocol version is an integer major version. Clients and servers choose
  exactly one common major version; unknown object fields and event types must
  be ignored by clients within a selected major version.
- Methods listed here but not yet implemented return `unsupported_capability`.
  Clients must check `manager.get` capabilities instead of inferring behavior
  from the operating system.
- Existing `--status=json`, `--doctor --json`, logs, and metrics remain
  operator interfaces, not substitutes for this versioned API.

## Local transport and limits

API v1 uses a Unix-domain stream socket at:

```text
$XDG_RUNTIME_DIR/kmonad-device-manager/api.sock
```

When `XDG_RUNTIME_DIR` is absent, use the manager's existing fallback base
(`~/.config/kmonad-device-manager`). The directory and socket must be owned by
the manager user and use modes `0700` and `0600`, respectively.

The listener starts independently after the manager has acquired its normal
single-instance lock. If endpoint creation, peer verification, a client, or a
request fails, the manager logs the API-specific failure and continues normal
reconciliation. On manager shutdown it closes client connections and removes
the socket. It never exposes API access through the metrics listener.

The server must verify Unix peer credentials and accept only an effective UID
matching the manager process; unauthorised peers are closed without a protocol
response. The API must not listen on TCP, share the metrics listener, or be
exposed through a network proxy.

| Limit | Value | Behavior |
|---|---:|---|
| Frame size | 1 MiB | Reject with `invalid_request` when possible; otherwise close. |
| Connected clients | 64 | Close excess connections without affecting accepted clients. |
| In-flight requests | 32 per client | Reply with `resource_exhausted`. |
| Manager command queue | 64 | Reply with `resource_exhausted`; reconciliation remains available. |
| Event backlog | 1,024 per subscriber | Send `manager.resync_required`, then end the subscription. |
| Default deadline | 30 seconds | Clients may request a shorter deadline. |

Connection close never cancels an accepted mutation. Cancellation is only
available through a documented cancellable operation method, initially
`device.identify.cancel`; unsafe cancellation returns `operation_not_cancellable`.
Before resource methods are implemented, their requests return immediately with
`unsupported_capability`; the transport still validates frame and deadline
bounds.

## Wire protocol

Messages are UTF-8 JSON objects framed as JSON Lines. Every message has a
`type` of `request`, `response`, or `event`.

### Request

```json
{
  "type": "request",
  "id": "01J00000000000000000000000",
  "method": "snapshot.get",
  "params": {},
  "deadline_ms": 10000,
  "idempotency_key": "01J00000000000000000000001"
}
```

| Field | Required | Meaning |
|---|---|---|
| `id` | yes | Opaque ID unique among in-flight requests on this connection. |
| `method` | yes | A v1 method from the method index. |
| `params` | yes | Method-specific object; `{}` when no parameters are defined. |
| `deadline_ms` | no | Positive deadline capped by the server limit. |
| `idempotency_key` | mutations | Opaque key used to safely return the original result after a retry. |

Responses contain the matching `id` and exactly one of `result` or `error`:

```json
{
  "type": "response",
  "id": "01J00000000000000000000000",
  "error": {
    "code": "stale_revision",
    "message": "configuration revision no longer matches",
    "details": {"current_revision": 43}
  }
}
```

Programs must use the stable `code` and `details`, not the display-oriented
`message`.

### Event

```json
{
  "type": "event",
  "event_id": 9001,
  "state_revision": 42,
  "time": "2026-09-22T12:00:00Z",
  "event_type": "configuration.runtime_changed",
  "resource": {"kind": "configuration", "id": "cfg_01J..."},
  "data": {}
}
```

`event_id` increases within a manager lifetime. `state_revision` increases for
every externally visible state change. Neither survives a manager restart as a
replacement for a fresh snapshot.

## Session and method index

The first request on every connection must be `session.hello`:

```json
{
  "supported_versions": [1],
  "client": {"name": "kmonad-device-manager-gui", "version": "0.1.0"}
}
```

Its result includes `selected_version`, `server_id`, `manager_version`, and the
initial `state_revision`. Any other first request receives
`invalid_request`.

| Method | Mutation | Purpose | Capability |
|---|---|---|---|
| `session.hello` | no | Negotiate API version; first request only. | none |
| `manager.get` | no | Read platform, backend, versions, limits, capabilities, and health. | none |
| `snapshot.get` | no | Read authoritative devices, configurations, diagnostics, operations, and revision. | none |
| `device.list` | no | List known keyboard-capable devices and detailed availability. | `device_discovery` |
| `configuration.list` | no | Inventory managed and read-only external configurations. | `managed_configurations` |
| `device.identify.start` | yes | Start a bounded keypress identification session. | `device_identification` |
| `device.identify.cancel` | yes | Cancel an identification session. | `device_identification` |
| `validation.preview` | no | Validate a candidate without persistence or runtime effect. | `candidate_validation` |
| `configuration.apply` | yes | Revalidate, persist, and activate one managed model. | `managed_configurations` |
| `configuration.create` | yes | Create a managed configuration. | `managed_configurations` |
| `configuration.update` | yes | Update a managed configuration candidate. | `managed_configurations` |
| `configuration.set_enabled` | yes | Enable or disable a managed configuration. | `managed_configurations` |
| `configuration.delete` | yes | Delete a managed configuration and stop it. | `managed_configurations` |
| `configuration.adopt` | yes | Explicitly adopt a representable external configuration. | `external_configuration_adoption` |
| `operation.get` | no | Read a validation, apply, rollback, or identify operation. | none |
| `events.subscribe` | no | Subscribe to ordered state-transition events. | `event_stream` |

`manager.get` includes a complete capability list. Initial Linux values may be
unavailable for unimplemented features; `multiple_independent_keyboards` and
`automatic_hotplug_recovery` already describe existing supervision behavior.

```json
{
  "api_versions": [1],
  "platform": "linux",
  "backend": "linux-evdev",
  "state_revision": 42,
  "capabilities": [
    {"name": "device_discovery", "available": true, "reason_code": "capability_available", "reason": "keyboard inventory is available"},
    {"name": "device_identification", "available": true, "reason_code": "capability_available", "reason": "keypress identification is available"},
    {"name": "candidate_validation", "available": true, "reason_code": "capability_available", "reason": "candidate validation is available"},
    {"name": "managed_configurations", "available": true, "reason_code": "capability_available", "reason": "transactional managed configuration apply is available"},
    {"name": "external_configuration_adoption", "available": false, "reason_code": "operation_unsupported", "reason": "external configuration adoption is not implemented"},
    {"name": "event_stream", "available": false, "reason_code": "operation_unsupported", "reason": "event streaming is not implemented"},
    {"name": "multiple_independent_keyboards", "available": true, "reason_code": "capability_available", "reason": "independent .kbd supervision is active"},
    {"name": "automatic_hotplug_recovery", "available": true, "reason_code": "capability_available", "reason": "configured devices are reconciled after reconnect"}
  ]
}
```

## Stable domain schema

All IDs are opaque manager-generated strings. Clients must not derive them from
filenames, device paths, product names, or process IDs.

Every domain object has machine-readable enums and reason codes plus
display-oriented text. Clients must use the enum and `reason_code` for logic;
`display_name`, `name`, `reason`, `summary`, `remediation`, and error `message`
may change without notice. The manager must always emit all required fields,
including an applicable reason code for every resource, operation, and event.

`reason_code` is lower snake case and describes _why_ an object has its current
state. It is not an API error `code`: API errors explain why a request could not
be completed, whereas reason codes explain resource state, validation outcome,
diagnostic findings, and operation progress. Unknown enum values and reason
codes must be rendered generically and otherwise ignored by v1 clients.

### Shared values

```json
{"kind": "configuration", "id": "cfg_01J..."}
```

`ResourceRef.kind` is `manager`, `device`, `configuration`, `operation`, or
`diagnostic`. Its ID is opaque except that the singleton manager resource uses
the server ID returned by `session.hello`.

### Device

```json
{
  "id": "dev_01J...",
  "display_name": "Example Keyboard",
  "vendor": "046d",
  "product": "c31c",
  "serial": "ABC123",
  "availability": "connected",
  "identity_stability": "serial",
  "configured_by": ["keyboard.kbd"],
  "runtime_conflict": false,
  "reason_code": "device_connected",
  "reason": "keyboard is connected and accessible"
}
```

`availability` is `connected`, `disconnected`, `inaccessible`, `unsupported`,
or `conflicting`. `identity_stability` is `serial`, `topology`, `platform`, or
`unknown`; it communicates identity confidence, not availability. A
disconnected device may remain known. Platform locators are not part of the
normal GUI contract. `vendor`, `product`, and `serial` are display metadata and
may be omitted when the platform cannot provide them.
`configured_by` contains external configuration names currently claiming the
device. `runtime_conflict` is true, and availability is `conflicting`, when
multiple configured keyboards claim the same live device. `inaccessible` means
the device node cannot be opened; `unsupported` means it is not a character
device.
Serial-backed identity incorporates vendor and product; if duplicate serial
identities are present simultaneously, the manager uses a topology fallback to
keep device IDs distinct.

### Identification operations

`device.identify.start` starts one bounded session for a connected, accessible
device. Its parameters are:

```json
{"device_id":"dev_01J...","timeout_ms":15000}
```

`device_id` is required. `timeout_ms` is optional and must be from 1,000 through
30,000; the default is 15,000. The result is an `operation` with kind `identify`
and state `waiting`. The manager accepts only one active identification session;
a second start returns `conflict`. It returns `temporary_unavailable` when the
device is absent or cannot be opened, and `not_found` for an unknown ID.

To observe keypresses when KMonad holds the device grab, the manager pauses only
the configuration process bound to the requested device. It reconciles that
configuration immediately when the operation succeeds, times out, is cancelled,
or the device disconnects. It never pauses an unrelated keyboard.

`device.identify.cancel` and `operation.get` both take:

```json
{"operation_id":"op_01J..."}
```

Cancellation returns the terminal `cancelled` operation. It returns
`operation_not_cancellable` if the operation is already terminal. A successful
keypress yields `succeeded` and `operation_succeeded`; a timeout yields `failed`
and `operation_timed_out`; a hotplug failure uses the applicable device
availability reason code.

### Configuration

```json
{
  "id": "cfg_01J...",
  "name": "Laptop keyboard",
  "ownership": "managed",
  "enabled": true,
  "device_id": "dev_01J...",
  "desired_revision": 7,
  "active_revision": 6,
  "runtime": {
    "phase": "running",
    "reason_code": "runtime_pending_update_rejected",
    "reason": "new configuration failed validation; running revision 6",
    "connected": true,
    "healthy": true,
    "failure_count": 1
  }
}
```

`ownership` is `managed` or `external`. Only managed configurations are
editable without explicit adoption. `RuntimeState.phase` is `discovered`,
`validating`, `waiting`, `applying`, `running`, `backoff`, `failed`,
`duplicate`, `stopped`, `disabled`, or `recovering`. `retry_at` is present only
for a scheduled retry. `desired_revision` and `active_revision` are separate so
rejected edits cannot appear as keyboard failure.

### Manager-owned configuration model

Future managed configuration create, update, and preview methods accept this
platform-neutral model before the manager renders a KMonad candidate:

```json
{
  "device_id": "dev_01J...",
  "behavior": "(defsrc a)\n(deflayer base a)"
}
```

`device_id` is mandatory and is resolved at render time. `behavior` must not
contain `defcfg` or `device-file`: the manager alone renders the Linux input
target after resolving the opaque ID. A stale ID is rejected; disconnected,
inaccessible, conflicting, or ambiguous resolution is returned as a structured
blocked validation result. Rendering is side-effect-free and never rewrites an
external `.kbd` file; candidate dry-run validation and persistence are separate
operations.

### Transactional managed apply

`configuration.apply` accepts a manager-owned model and either creates a new
configuration or updates an existing one:

```json
{"name":"Laptop keyboard","model":{"device_id":"dev_01J...","behavior":"(defsrc a)"}}
```

```json
{"configuration_id":"cfg_01J...","expected_revision":7,"model":{"device_id":"dev_01J...","behavior":"(defsrc a)"}}
```

New configurations require `name`. Updates require both `configuration_id` and
`expected_revision`; a mismatch returns `stale_revision`. The manager creates
an apply operation, renders and validates the candidate again (a prior preview
does not authorize apply), then writes a new immutable revision below its
manager-owned state directory. It does not write the external watched
configuration directory. The operation finishes `succeeded` only after the
new process starts, joins its cgroup, and passes the manager ownership and
health check; otherwise it returns `failed` while retaining the desired
revision. If an update fails activation, the manager restores the prior durable
metadata, stops the unconfirmed replacement, and restarts the prior immutable
revision. A recovered operation ends `rolled_back` with
`runtime_rollback_succeeded`; a restore or restart failure ends `failed` with
`runtime_rollback_failed`. A failed initial create removes its metadata rather
than leaving an unconfirmed managed mapping.
The returned operation includes `configuration_revision`, which callers use as
the next update's `expected_revision`.

`configuration.create` accepts the new-configuration form only (`name` and
`model`). `configuration.update` accepts the update form only
(`configuration_id`, `expected_revision`, and `model`, with optional `name`).
They use the identical apply pipeline. `configuration.apply` remains available
as the combined CLI-compatible form.

### Managed lifecycle

`configuration.set_enabled` accepts:

```json
{"configuration_id":"cfg_01J...","expected_revision":7,"enabled":false}
```

`configuration.delete` accepts:

```json
{"configuration_id":"cfg_01J...","expected_revision":8}
```

Both require the current revision and return `stale_revision` when it has
changed. Disabling stops only the target KMonad process while retaining its
immutable revision. Enabling restores that desired configuration to normal
reconciliation, including automatic keyboard reconnect recovery. Deleting
stops the target and removes its manager-owned metadata and revision files.
Each returns a terminal `lifecycle` operation with the resulting
`configuration_revision`; delete returns zero because no managed revision
remains.

### Configuration inventory

`configuration.list` takes `{}` and returns `{ "configurations": [...] }`.
Each item is a `Configuration` domain object without platform paths or rendered
KMonad bytes. External `.kbd` configurations are present with
`ownership: "external"` and are read-only to all lifecycle methods. The
manager stores their private path and content signature only in its state
directory sidecar so it can maintain a stable opaque ID while the file exists.

Manager-owned items have `ownership: "managed"`, their model revision, and
their current desired/active state. If the immutable revision bytes no longer
match manager metadata, inventory reports `runtime.phase: "failed"` and
`configuration_changed`; future update and enable requests are rejected until
the user restores or deletes that managed configuration. The manager never
silently replaces altered bytes.

### ValidationResult

```json
{
  "outcome": "blocked",
  "reason_code": "validation_blocked",
  "reason": "the selected keyboard is disconnected",
  "diagnostics": [
    {
      "id": "device.availability",
      "severity": "temporary",
      "reason_code": "device_disconnected",
      "summary": "Laptop keyboard is disconnected",
      "remediation": "Reconnect the keyboard, then retry.",
      "resource": {"kind": "device", "id": "dev_01J..."}
    }
  ]
}
```

`outcome` is `valid`, `rejected`, or `blocked`. `valid` means the candidate
passed all checks. `rejected` means a non-retryable candidate or policy error.
`blocked` means a temporary condition prevents a decision; it does not imply
that candidate data is invalid. Validation never exposes a platform file path
or command line to normal clients.

### Candidate validation preview

`validation.preview` accepts exactly one of a manager-owned model or KMonad
candidate text:

```json
{"model":{"device_id":"dev_01J...","behavior":"(defsrc a)"}}
```

```json
{"content":"(defcfg input (device-file \"/dev/input/event0\"))"}
```

The result is `{ "validation": ValidationResult }`. Models resolve the opaque
device ID and render the platform input target inside the manager. Content is
limited by `KMONAD_MAX_CONFIG_BYTES`; the manager checks input availability and
existing device claims before running `kmonad --dry-run` against an immutable
snapshot in its runtime directory. The snapshot is removed in every outcome.
Preview never writes a watched configuration directory, starts a KMonad mapping,
or changes current supervision. A dry-run timeout returns a blocked result with
`validation_timed_out`; KMonad syntax failures return a rejected result with
`validation_failed`.

### Diagnostic

Diagnostics contain a stable `id`, `severity` (`ok`, `warning`, `temporary`, or
`error`), stable `reason_code`, display summary, remediation, and optional
affected resource. An unchanged diagnostic must retain its ID across snapshots
within one manager lifetime. A client may group diagnostics by `reason_code` but
must retain ID-level distinctions for multiple affected resources.

### Capability

Each known capability is returned even when unavailable:

```json
{
  "name": "candidate_validation",
  "available": false,
  "reason_code": "operation_unsupported",
  "reason": "candidate validation is not implemented"
}
```

`name` is one of `device_discovery`, `device_identification`,
`candidate_validation`, `managed_configurations`,
`external_configuration_adoption`, `event_stream`,
`multiple_independent_keyboards`, or `automatic_hotplug_recovery`.

### Operation

```json
{
  "id": "op_01J...",
  "kind": "apply",
  "state": "running",
  "resource": {"kind": "configuration", "id": "cfg_01J..."},
  "started_at": "2026-09-22T12:00:00Z",
  "updated_at": "2026-09-22T12:00:02Z",
  "configuration_revision": 7,
  "reason_code": "operation_running",
  "reason": "validating candidate"
}
```

`kind` is `identify`, `validate`, `apply`, `rollback`, `adopt`, or `lifecycle`.
`state` is `queued`, `running`, `waiting`, `cancelling`, `succeeded`,
`rejected`, `failed`, `rolled_back`, or `cancelled`. The last five states are
terminal. A completed validation operation includes `validation`; other
operation-specific results are defined by their method before implementation.

### Event

The event envelope above is the `Event` domain type. `event_type` is one of the
stable names in [Snapshot and events](#snapshot-and-events); `reason_code`
explains the transition. `data` contains only event-type-specific, documented
fields and never replaces a fresh snapshot as the source of truth.

### Reason-code index

The following codes are stable in API v1. New codes may be added; existing
codes must not change meaning.

| Area | Reason codes |
|---|---|
| Device | `device_connected`, `device_disconnected`, `device_inaccessible`, `device_unsupported`, `device_conflicting`, `device_identity_ambiguous` |
| Configuration | `configuration_discovered`, `configuration_disabled`, `configuration_external_read_only`, `configuration_adoption_required`, `configuration_revision_stale`, `configuration_limit_reached`, `configuration_changed`, `configuration_too_large` |
| Validation | `validation_succeeded`, `validation_failed`, `validation_timed_out`, `validation_blocked`, `candidate_unsupported` |
| Runtime | `runtime_starting`, `runtime_running`, `runtime_waiting_for_device`, `runtime_backoff`, `runtime_process_exited`, `runtime_watchdog_timeout`, `runtime_process_unhealthy`, `runtime_ownership_lost`, `runtime_duplicate_device`, `runtime_pending_update_rejected`, `runtime_activation_failed`, `runtime_rollback_succeeded`, `runtime_rollback_failed`, `runtime_stopped` |
| Operation/capability | `operation_queued`, `operation_running`, `operation_succeeded`, `operation_cancelled`, `operation_timed_out`, `operation_unsupported`, `capability_available` |
| Dependency/safety | `dependency_unavailable`, `permission_denied`, `internal` |

## Mutation safety

Durable configuration mutations require an `idempotency_key`, an
`expected_revision` for an existing configuration, and a manager-owned
`device_id` rather than a platform path. The manager returns `stale_revision`
rather than silently applying a stale GUI edit. Retrying an idempotency key
returns the original operation. Identification is ephemeral rather than durable:
it is serialized as one active session and does not require a revision or
idempotency key.

Apply always revalidates immediately before activation, even after a successful
preview. The manager keeps the active known-good revision until replacement
validation, launch, isolation, and early health confirmation succeed. It must
restore the prior revision on failure. A disconnected or crashed GUI never
cancels an accepted apply unless it explicitly cancels a documented cancellable
operation.

## Snapshot and events

`snapshot.get` is the recovery source of truth and returns `state_revision`,
manager state, capabilities, devices, configurations, diagnostics, and active
operations. `events.subscribe` accepts optional `after_event_id`; it replays
retained events or sends `manager.resync_required` and ends the subscription.
Clients then fetch a snapshot and resubscribe. Slow clients must never block
reconciliation.

Stable event names are:

```text
device.added
device.availability_changed
configuration.changed
configuration.runtime_changed
configuration.rejected
configuration.rolled_back
operation.changed
diagnostic.changed
manager.resync_required
```

## Error codes

| Code | Meaning |
|---|---|
| `invalid_request` | Message, method, ID, or parameters are malformed. |
| `unsupported_version` | No mutually supported protocol version exists. |
| `unsupported_capability` | The requested feature is unavailable on this manager/platform. |
| `unauthorized_peer` | Unix peer credentials do not match the manager user. |
| `resource_exhausted` | A connection or manager limit was exceeded. |
| `deadline_exceeded` | The request did not complete before its deadline. |
| `not_found` | The opaque resource ID does not exist. |
| `stale_revision` | The expected durable revision no longer matches. |
| `conflict` | A configuration or device conflicts with another managed resource. |
| `temporary_unavailable` | A retryable condition, such as a disconnected keyboard, blocks completion. |
| `validation_failed` | Candidate validation rejected the configuration. |
| `operation_not_cancellable` | Cancellation is unsafe at the operation's current phase. |
| `internal` | Unexpected manager failure; details must not leak secrets or unneeded paths. |

## Implementation acceptance criteria

- Same-user clients complete `session.hello` before all other methods.
- API handlers submit commands to the single reconciliation owner; they do not
  mutate manager state concurrently.
- Mutations are revision-checked and idempotent.
- A snapshot rebuilds client state after event loss, disconnect, restart, or
  `manager.resync_required`.
- Capabilities, not client OS checks, govern feature availability.
- No client can use the API to access another user's manager, arbitrary paths,
  arbitrary commands, or a network listener.
