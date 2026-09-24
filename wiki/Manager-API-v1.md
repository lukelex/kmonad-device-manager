# Manager API v1

**Status:** The same-user Unix-socket API v1 and the resource methods indexed
below are implemented on the Linux backend. Capability responses describe
runtime availability; non-Linux backends remain unavailable.

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
- Clients must check `manager.get` capabilities rather than infer availability
  from the operating system. Unavailable capabilities return
  `unsupported_capability` for operations requiring them.
- Existing `--status --json`, `--doctor --json`, logs, and metrics remain
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
Resource requests validate frame and deadline bounds before execution. Methods
that require an unavailable platform capability return `unsupported_capability`.

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
| `snapshot.get` | no | Read authoritative devices, configurations, retained operations, diagnostics, health, and revision. | none |
| `device.list` | no | List known keyboard-capable devices and detailed availability. | `device_discovery` |
| `configuration.list` | no | Inventory managed and read-only external configurations. | `managed_configurations` |
| `configuration.content.get` | no | Read bounded external UTF-8 source at an expected content revision. | `configuration_content_read` |
| `configuration.export` | no | Export the immutable manager-rendered managed KMonad artifact. | `configuration_export` |
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

`manager.get` includes a complete capability list. Linux reports its supported
discovery, identification, validation, managed lifecycle, event, and independent
keyboard supervision features; clients still inspect each capability at runtime.

```json
{
  "api_versions": [1],
  "manager_version": "1.1.0",
  "server_id": "srv_01J...",
  "platform": "linux",
  "platform_version": "6.12.0",
  "backend": "linux-evdev",
  "backend_version": "evdev",
  "kmonad": {"available":true,"version":"0.4.1","compatibility":"compatible","reason_code":"kmonad_compatible","reason":"KMonad version supports the manager validation lifecycle"},
  "state_revision": 42,
	"event_cursor": {"server_id":"srv_01J...", "event_id":9001, "state_revision":42},
	"limits": {
	  "max_configurations": 128,
	  "max_configuration_bytes": 1048576,
	  "command_queue": 64,
	  "event_history": 1024,
	  "event_subscriber_queue": 1024,
	  "api_max_clients": 64,
	  "api_in_flight_requests": 32,
	  "api_frame_bytes": 1048576,
	  "default_deadline_ms": 30000
	},
  "capabilities": [
    {"name": "device_discovery", "available": true, "reason_code": "capability_available", "reason": "available on the Linux evdev backend"},
    {"name": "device_identification", "available": true, "reason_code": "capability_available", "reason": "available on the Linux evdev backend"},
    {"name": "candidate_validation", "available": true, "reason_code": "capability_available", "reason": "available on the Linux evdev backend"},
    {"name": "managed_configurations", "available": true, "reason_code": "capability_available", "reason": "available on the Linux evdev backend"},
    {"name": "external_configuration_adoption", "available": true, "reason_code": "capability_available", "reason": "available on the Linux evdev backend"},
    {"name": "event_stream", "available": true, "reason_code": "capability_available", "reason": "available on the Linux evdev backend"},
    {"name": "multiple_independent_keyboards", "available": true, "reason_code": "capability_available", "reason": "available on the Linux evdev backend"},
    {"name": "automatic_hotplug_recovery", "available": true, "reason_code": "capability_available", "reason": "available on the Linux evdev backend"},
    {"name": "per_device_mapping", "available": true, "reason_code": "capability_available", "reason": "available on the Linux evdev backend"},
    {"name": "input_target_device_file", "available": true, "reason_code": "capability_available", "reason": "available on the Linux evdev backend"},
    {"name": "configuration_content_read", "available": true, "reason_code": "capability_available", "reason": "available on the Linux evdev backend"},
    {"name": "configuration_export", "available": true, "reason_code": "capability_available", "reason": "available on the Linux evdev backend"}
  ],
  "limitations": [{"id":"input.device_file_only","reason_code":"candidate_unsupported","summary":"Only KMonad device-file input targets are supported.","remediation":"Use a device-file input or a manager-owned configuration model."}],
  "health": {"healthy":true,"reason_code":"manager_healthy","reason":"manager reconciliation owner is responsive","reconcile_count":12,"failure_count":0,"metrics_available":true,"status_write_failures":0}
}
```

`manager.get` is an owner-consistent metadata read. Its `event_cursor` has the
same manager-instance semantics as the cursor returned by `snapshot.get` and
can be used to resume `events.subscribe`. `limits` contains only public API and
retention limits; it never reveals manager filesystem locations, device nodes,
process IDs, cgroup paths, or metrics listener settings. `health` is the
public `ManagerHealth` domain object.

### ManagerInfo

`ManagerInfo` is the `manager.get` result. `api_versions` always contains the
versions supported by this server. `server_id` is regenerated for every
manager/API-server lifetime and is duplicated in `event_cursor.server_id`.
`kmonad` is a bounded startup version probe plus current executable
availability. `compatible` means the reported semantic version is 0.4.0 or
newer; `platform_version` and `backend_version` describe the selected backend
without exposing a filesystem path. `incompatible`, `unknown`, and `unavailable` require the client to
render the supplied reason and remediation diagnostic. `capabilities` contains every stable `CapabilityName`, in the documented
order, with availability and an explanatory reason. `limitations` are stable
supported-feature boundaries, distinct from transient diagnostics. The response's public
limits are fixed for that manager lifetime; callers must not assume configured
limits from another server instance still apply after a reconnect.

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
  "role": "input",
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

`role` is `input` or `manager_output`. `manager_output` identifies a
manager-created KMonad `uinput-sink` virtual device. It is retained for accurate
inventory but cannot be identified or used for preview, apply, or lifecycle
input configuration; clients must not render it as a configurable keyboard.
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
device is absent or cannot be opened, `not_found` for an unknown ID, and
`device_not_configurable` for a `manager_output` device.

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
  },
  "last_operation": {
    "id": "op_01J...",
    "kind": "apply",
    "state": "rolled_back",
    "resource": {"kind": "configuration", "id": "cfg_01J..."},
    "reason_code": "runtime_rollback_succeeded",
    "reason": "activation failed; the previous revision was restored"
  }
}
```

`ownership` is `managed` or `external`. Only managed configurations are
editable without explicit adoption. `RuntimeState.phase` is `discovered`,
`validating`, `waiting`, `applying`, `running`, `backoff`, `failed`,
`duplicate`, `stopped`, `disabled`, or `recovering`. `retry_at` is present only
for a scheduled retry. `desired_revision` is the durable managed model or
lifecycle revision; `active_revision` is the last immutable content revision
whose process passed the manager health check. It remains available while that
process is temporarily stopped, disconnected, or a later candidate is rejected.
An active revision of zero means no managed revision has passed health
confirmation yet. `last_operation`, when retained, is the most recently updated
operation for that configuration and exposes a pending, rejected, rolled-back,
or successful candidate without changing the known-good active revision.

### Authoritative snapshot

`snapshot.get` takes `{}` and returns a single owner-consistent public state
document:

```json
{
  "state_revision": 43,
  "event_cursor": {"server_id":"srv_01J...", "event_id":9001, "state_revision":43},
  "devices": [{"id":"dev_01J...", "availability":"connected"}],
  "configurations": [{"id":"cfg_01J...", "ownership":"managed"}],
  "operations": [{"id":"op_01J...", "state":"succeeded"}],
  "diagnostics": [{"id":"configuration.cfg_01J....runtime", "severity":"ok", "reason_code":"runtime_running", "summary":"configuration is running", "remediation":"No action is required.", "resource":{"kind":"configuration","id":"cfg_01J..."}}],
  "health": {
    "healthy": true,
    "reason_code": "manager_healthy",
    "reason": "manager reconciliation owner is responsive",
    "last_progress_at": "2026-09-23T12:00:00Z",
    "reconcile_count": 19,
    "failure_count": 0,
    "metrics_available": false,
    "status_write_failures": 0
  }
}
```

`devices` includes connected-unconfigured and persisted known-disconnected
records. `configurations` includes managed and external inventory records.
`operations` contains retained operations, ordered by update time and opaque ID;
the per-configuration `last_operation` identifies the relevant latest record.
`diagnostics` contains owner-derived manager, device, and configuration checks,
ordered by stable diagnostic ID. Clients render the supplied summary and
remediation but make decisions from severity and reason code.
`state_revision` increases monotonically for the lifetime of one manager
instance when reconciliation, snapshot refresh, or event publication exposes a
state transition; clients use the negotiated server ID to detect a manager
restart. A snapshot response never includes platform paths,
device nodes, process IDs, rendered KMonad bytes, or manager state locations.
Failure to obtain a snapshot affects only that caller and never blocks
reconciliation.

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
contain `defcfg`, `device-file`, `uinput-sink`, or another manager-owned
input/output form: the manager alone renders the complete platform-owned
configuration header after resolving the opaque ID. On Linux evdev this includes
the resolved device-file input and a manager-selected per-device uinput output.
A stale ID is rejected; disconnected, inaccessible, conflicting, ambiguous, or
output-unavailable resolution is returned as a structured blocked validation
result. Rendering is side-effect-free and never rewrites an external `.kbd`
file; candidate dry-run validation and persistence are separate operations.

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
External inventory also returns `content_revision`: a positive, JSON-exact
content-derived token (not an incrementing sequence), stable across manager
restart and removal/recreation with identical bytes. Clients use it for
`configuration.content.get` freshness checks.

Manager-owned items have `ownership: "managed"`, their model revision, and
their current desired/active state. If the immutable revision bytes no longer
match manager metadata, inventory reports `runtime.phase: "failed"` and
`configuration_changed`; future update and enable requests are rejected until
the user restores or deletes that managed configuration. The manager never
silently replaces altered bytes.

### Configuration content and export

`configuration.content.get` accepts an external `configuration_id` and positive
`expected_revision` from inventory, and returns only UTF-8 text:

```json
{"configuration_id":"cfg_external","ownership":"external","content_revision":12345,"digest":"sha256:…","content":"(defcfg …)\n"}
```

Only the same-user local API may read content. The manager limits file and
response sizes, rejects symlinks and file replacement during reading, and
returns `stale_revision` when the expected external content token differs.
Unknown IDs return `not_found`; changed or oversized content returns a
structured error. Managed source is not readable through this method. Reading
external text does not imply visual import, adoption, or write permission.

`configuration.export` accepts a managed `configuration_id` and the explicit
format `manager_rendered_kbd`:

```json
{"configuration_id":"cfg_managed","format":"manager_rendered_kbd"}
```

Its response includes `configuration_id`, the current managed `revision`, the
same `format`, SHA-256 `digest`, and `content`. Bytes come from the immutable
manager-owned revision, with integrity verified before return. The artifact
contains manager-selected device input/output forms; it is specific to this
manager and device binding, not a portable GUI profile. Other formats and
external export are rejected. Neither read starts operations or changes
supervision. The CLI equivalents are `config read EXTERNAL_CONFIGURATION_ID
CONTENT_REVISION --json` and `config export CONFIGURATION_ID
manager_rendered_kbd --json`.

### External configuration adoption

`configuration.adopt` accepts an external configuration ID from
`configuration.list` and an optional managed display name:

```json
{"configuration_id":"cfg_01J...","name":"Imported keyboard"}
```

It accepts only a losslessly representable external source: one `defcfg` whose
only option is `input (device-file "...")`, with no remaining `defcfg` or
`device-file` form in its behavior. The manager resolves the source device to
the external record's opaque `device_id`, renders its own managed input form,
and validates it as a new immutable managed configuration. The external source
is never written, renamed, or deleted.

The manager verifies the external content digest immediately before hand-off.
Only after the managed revision is durable does it record a private
signature-gated hand-off and stop supervising that unchanged external source.
Activation still must pass normal process/cgroup/health confirmation. Failure
removes the new managed revision and restores external supervision. Editing the
source after adoption changes its signature and returns it to normal external,
read-only supervision; it never overwrites the managed revision.
Deleting the adopted managed configuration also restores supervision of an
unchanged external source.

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

For a model preview, `candidate_digest` is `sha256:` followed by the SHA-256
digest of the exact UTF-8 `model.behavior` bytes supplied by the client, before
manager rendering or normalization. It is omitted for raw `content` previews.
It binds a returned validation to a candidate; the JSON Lines request ID remains
the request/response correlation mechanism.

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

An error diagnostic from a model preview may include a location:

```json
{"scope":"submitted_behavior","start_line":14,"start_column":3,"end_line":14,"end_column":31}
```

Locations are optional. A missing location means unmapped, never a default key.
The supported `submitted_behavior` scope uses 1-based, half-open line/column
ranges in the exact submitted UTF-8 `model.behavior` text, not in the generated
KMonad candidate. The manager emits it only for a range wholly within one
submitted `deflayer` assignment; wrapper, input/output rendering, device,
permission, conflict, dependency, timeout, runtime, ambiguous, and unknown
regions have no location. Clients must treat unknown future scopes as unmapped.
Only `rejected` validation may carry this assignment location; `blocked` never
does.

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
  "available": true,
  "reason_code": "capability_available",
  "reason": "available on the Linux evdev backend"
}
```

`name` is one of `device_discovery`, `device_identification`,
`candidate_validation`, `managed_configurations`,
`external_configuration_adoption`, `event_stream`,
`configuration_content_read`, `configuration_export`,
`multiple_independent_keyboards`, `automatic_hotplug_recovery`,
`per_device_mapping`, or `input_target_device_file`.

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
| Device | `device_connected`, `device_disconnected`, `device_inaccessible`, `device_unsupported`, `device_conflicting`, `device_identity_ambiguous`, `device_manager_output` |
| Configuration | `configuration_discovered`, `configuration_disabled`, `configuration_external_read_only`, `configuration_adoption_required`, `configuration_revision_stale`, `configuration_limit_reached`, `configuration_changed`, `configuration_too_large` |
| Validation | `validation_succeeded`, `validation_failed`, `validation_timed_out`, `validation_blocked`, `candidate_unsupported` |
| Runtime | `runtime_starting`, `runtime_running`, `runtime_waiting_for_device`, `runtime_backoff`, `runtime_process_exited`, `runtime_watchdog_timeout`, `runtime_process_unhealthy`, `runtime_ownership_lost`, `runtime_duplicate_device`, `runtime_pending_update_rejected`, `runtime_activation_failed`, `runtime_rollback_succeeded`, `runtime_rollback_failed`, `runtime_stopped` |
| Manager | `manager_healthy`, `manager_starting`, `manager_resync_required`, `kmonad_compatible`, `kmonad_version_unknown`, `kmonad_version_unsupported` |
| Diagnostic | `diagnostic_resolved` |
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

`configuration.apply`, `configuration.create`, `configuration.update`,
`configuration.set_enabled`, `configuration.delete`, and `configuration.adopt`
require a non-empty opaque key of at most 128 UTF-8 bytes. The manager hashes
the key and a canonical JSON representation of the method and complete params;
JSON object member order is irrelevant. Reuse with different parameters or a
different method returns `idempotency_conflict` without another mutation.
Accepted operations are recorded in a private, versioned 0600 journal before
execution and their final results are persisted before the response is sent.
Disconnecting the client does not cancel an admitted mutation. `operation.get`
and `snapshot.get` retain keyed operations across restart. At most 128 keys are
retained; admitting a new key evicts the oldest finished record and its operation,
never a pending one. A manager interruption before the final outcome was
persisted leaves the same operation ID in a terminal failed state with an
explicit uncertain-outcome reason; retrying that key never executes the mutation
again. Refresh the configuration snapshot before deciding on a new operation.

Apply always revalidates immediately before activation, even after a successful
preview. The manager keeps the active known-good revision until replacement
validation, launch, isolation, and early health confirmation succeed. It must
restore the prior revision on failure. A disconnected or crashed GUI never
cancels an accepted apply unless it explicitly cancels a documented cancellable
operation.

## Snapshot and events

`snapshot.get` is the recovery source of truth and returns `state_revision`,
manager state, devices, configurations, retained operations, diagnostics, and
health. Snapshot diagnostics are derived by the reconciliation owner from the
same public device, configuration, and manager-health state; they never require
a GUI client or an API request to keep supervision running.
`events.subscribe` accepts optional `after_event_id` and `after_server_id` from
the latest snapshot cursor:

```json
{"after_event_id":42,"after_server_id":"srv_01J..."}
```

It first responds with `subscription_id` and `state_revision`, then writes
ordered JSON Lines `event` frames on the same connection. A supplied cursor
replays retained events with a larger `event_id` before live events; an omitted
cursor follows new events only. A cursor whose server ID differs from the
current manager, is ahead of the current event ID, or predates retention cannot
be resumed and receives resynchronization. The manager retains 1,024 events and gives each
subscriber a 1,024-event non-blocking queue. History older than retention, or a
slow subscriber queue overflow, produces one `manager.resync_required` event
and closes that subscription. Clients then fetch a snapshot and resubscribe.
Slow clients never block reconciliation or unrelated subscribers.

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

The manager publishes each public transition directly to its retained event
stream, operator metrics, and (when `KMONAD_LOG_FORMAT=json`) a sanitized
`manager_transition` JSON log record. The log record includes `event_id`,
`state_revision`, `event_type`, `resource_kind`, opaque `resource_id`, and
`reason_code`; it excludes event payload data, platform paths, process IDs, and
KMonad output. Prometheus exposes the latest public revision as
`kmonad_manager_public_state_revision` and transition counts as
`kmonad_manager_public_events_total{event_type=...}`. Operators must use these
surfaces rather than parse human-readable logs to derive state.

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
| `idempotency_conflict` | The key was previously accepted with a different method or parameters. |
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
