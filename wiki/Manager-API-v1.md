# Manager API v1

**Status:** Contract specification; not implemented yet.

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

The server must verify Unix peer credentials and accept only an effective UID
matching the manager process. The API must not listen on TCP, share the metrics
listener, or be exposed through a network proxy.

| Limit | Value | Behavior |
|---|---:|---|
| Frame size | 1 MiB | Reject with `invalid_request` when possible; otherwise close. |
| In-flight requests | 32 per client | Reply with `resource_exhausted`. |
| Event backlog | 1,024 per subscriber | Send `manager.resync_required`, then end the subscription. |
| Default deadline | 30 seconds | Clients may request a shorter deadline. |

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
| `device.list` | no | List known and connected devices. | `device_discovery` |
| `device.identify.start` | yes | Start a bounded keypress identification session. | `device_identification` |
| `device.identify.cancel` | yes | Cancel an identification session. | `device_identification` |
| `validation.preview` | no | Validate a candidate without persistence or runtime effect. | `candidate_validation` |
| `configuration.create` | yes | Create a managed configuration. | `managed_configurations` |
| `configuration.update` | yes | Update a managed configuration candidate. | `managed_configurations` |
| `configuration.set_enabled` | yes | Enable or disable a managed configuration. | `managed_configurations` |
| `configuration.delete` | yes | Delete a managed configuration and stop it. | `managed_configurations` |
| `configuration.adopt` | yes | Explicitly adopt a representable external configuration. | `external_configuration_adoption` |
| `operation.get` | no | Read a validation, apply, rollback, or identify operation. | none |
| `events.subscribe` | no | Subscribe to ordered state-transition events. | `event_stream` |

`manager.get` includes stable capability booleans. Initial Linux values may be
false for unimplemented features; `multiple_independent_keyboards` and
`automatic_hotplug_recovery` already describe existing supervision behavior.

```json
{
  "api_versions": [1],
  "platform": "linux",
  "backend": "linux-evdev",
  "state_revision": 42,
  "capabilities": {
    "device_discovery": false,
    "device_identification": false,
    "candidate_validation": false,
    "managed_configurations": false,
    "external_configuration_adoption": false,
    "event_stream": false,
    "multiple_independent_keyboards": true,
    "automatic_hotplug_recovery": true
  }
}
```

## Public resources

All IDs are opaque manager-generated strings. Clients must not derive them from
filenames, device paths, product names, or process IDs.

### Device

```json
{
  "id": "dev_01J...",
  "display_name": "Example Keyboard",
  "availability": "connected",
  "identity_stability": "serial",
  "configured_by": ["cfg_01J..."],
  "runtime_conflict": false
}
```

`availability` is `connected`, `disconnected`, `inaccessible`, `unsupported`,
or `conflicting`. A disconnected device may remain known. Platform locators are
not part of the normal GUI contract.

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
    "state": "running",
    "reason_code": "pending_update_rejected",
    "reason": "new configuration failed validation; running revision 6",
    "connected": true,
    "healthy": true
  }
}
```

`ownership` is `managed` or `external`. Only managed configurations are
editable without explicit adoption. Runtime state is `discovered`,
`validating`, `waiting`, `applying`, `running`, `backoff`, `failed`,
`duplicate`, `stopped`, or `disabled`. `desired_revision` and
`active_revision` are separate so rejected edits cannot appear as keyboard
failure.

### Diagnostics and operations

Diagnostics contain stable `id`, `severity` (`ok`, `warning`, `temporary`, or
`error`), `summary`, `remediation`, and affected resource. Operations contain
opaque `id`, `kind`, resource, start time, and state. Terminal states are
`succeeded`, `rejected`, `failed`, `rolled_back`, and `cancelled`; they include
a result or structured error.

## Mutation safety

Every mutation requires an `idempotency_key`, an `expected_revision` for an
existing configuration, and a manager-owned `device_id` rather than a platform
path. The manager returns `stale_revision` rather than silently applying a
stale GUI edit. Retrying an idempotency key returns the original operation.

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
