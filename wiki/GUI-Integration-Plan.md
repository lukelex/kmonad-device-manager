# GUI Integration Gap Analysis and Task Plan

Assessment date: 2026-09-22

## Goal and scope

The GUI must be a client of `kmonad-device-manager`, not the owner of the
manager process. The GUI owns the editable keyboard-behavior model; the manager
owns device identity, platform translation, validation, activation, KMonad
processes, runtime state, and diagnostics.

Breaking changes to the evolving GUI/API contract are acceptable. They must not
change the existing systemd service's independent supervision, recovery, and
failure-safety behavior for external `.kbd` files.

This repository currently supports Linux with a systemd user service. A Linux
GUI can be delivered before other platform backends, but the manager must
report that limitation instead of making the GUI infer it.

Status labels below mean:

- **Implemented**: the requirement is substantially met by the current
  file/CLI interface.
- **Partial**: useful machinery exists, but it does not yet satisfy the GUI
  contract or an important safety case is missing.
- **Missing**: no usable implementation exists today.

## Current interface and reusable foundation

The current service scans a configuration directory and treats `.kbd` file
changes as apply requests. Its client-facing surfaces are CLI output,
`--status=json`, a private `status.json` runtime file, journal/log output, and
optional Prometheus metrics. There is no local control API, event subscription,
device inventory, or machine-readable diagnostics contract. Core manager state
and service logic live in `internal/manager`; OS primitives are delegated to
`internal/platform`.

The following foundations should be retained rather than reimplemented:

- [x] Per-configuration KMonad supervision, independent restart/backoff, and
  duplicate runtime-device prevention in
  `internal/manager/supervisor.go`.
- [x] Event-driven configuration/device-directory watching with polling
  fallback in `internal/manager/runtime.go`.
- [x] Dry-run validation of immutable, size-bounded snapshots before every
  launch in `internal/manager/state.go` and
  `internal/manager/supervisor.go`.
- [x] Preservation of a running known-good process when a changed file fails
  dry-run validation.
- [x] Process groups, parent-death handling, pidfd-aware signaling, ownership
  checks, stale-process recovery, watchdog integration, and optional cgroups.
- [x] Atomic runtime status persistence and JSON status output with connection,
  health, retry, and failure details.
- [x] Environment checks in `internal/manager/doctor.go`, including
  KMonad, groups, `uinput`, configuration security, input availability,
  parsing, and service state.
- [x] Independent handling of multiple `.kbd` files and continued support for
  configurations authored outside a future GUI.
- [x] GUI-independent startup and operation through the systemd user service.

## Requirements cross-reference

| # | Requirement | Status | What exists now | Remaining gap |
|---|---|---|---|---|
| 1 | Device discovery | **Missing** | Configured device paths are parsed from `.kbd` files; `deviceReady` checks character-device access. | The manager cannot enumerate unconfigured keyboards or retain a known-disconnected inventory. `deviceID` is the current node's `rdev`, which is useful for runtime conflict detection but is not a stable physical identity across reconnects. |
| 2 | Keyboard identification | **Missing** | No low-level input observation or identification session exists. | Add a bounded, cancellable manager operation that attributes a key event to a discovered keyboard and coordinates with an existing KMonad grab when necessary. |
| 3 | Device availability | **Partial** | Reconciliation and `refreshWatches` react to configured device connection, disconnection, and symlink-target replacement without a manager restart. Status reports `connected`; stable `/dev/input/by-id` config paths can reconnect automatically. | Report connected-unconfigured and known-disconnected devices. Separate missing, permission-denied, wrong-device-type, and other unavailable reasons; status currently collapses them into `device unavailable`. |
| 4 | Candidate validation | **Partial** | Every launch uses `validateConfigSnapshot` and `kmonad --dry-run`; `doctor` validates files and checks the environment. | No public operation accepts an unapplied candidate. Results are not structured as error/temporary/warning, and validation is tied to a file containing a Linux `device-file` path. Conflict, capability, and dependency checks need one reusable validation pipeline. |
| 5 | Applying configuration | **Partial** | File changes are picked up automatically, revalidated, and affect only their configuration. Invalid edits leave the old process running. | There is no explicit atomic apply operation, revision check, enable/disable operation, or managed persistence. After successful dry-run, the old process is stopped before the replacement starts; a start/cgroup failure does not restore the old configuration. Final validation must remain mandatory even after an earlier preview validation. |
| 6 | Process supervision | **Implemented** | `reconcile`, `startConfig`, `stopProcess`, backoff, watchdog checks, duplicate detection, process ownership checks, and cleanup cover the required lifecycle. The manager is independent of any GUI. | Expose operations and state through the future API without allowing clients to manipulate PIDs. Add explicit desired enable/disable state rather than requiring file removal. |
| 7 | Runtime status | **Partial** | `status.json` and `--status=json` expose per-config state, connectivity, health, PID, reason, failures, retry time, and last-known-good signature. Health uses more than process existence. | Define a versioned public schema with stable IDs and reason codes. Model desired revision, active revision, pending validation/apply, and availability separately; the current state can report a healthy old process while only failure fields reveal that a new revision was rejected. |
| 8 | Runtime changes and events | **Missing** | Internal fsnotify events trigger reconciliation, and structured logs emit several lifecycle event names. A client can repeatedly reconstruct state from `status.json`. | Logs are not a complete or stable event API. Add subscriptions with ordered event IDs, reconnect/resync behavior, and events for device, configuration, process, dependency, and diagnostic transitions. |
| 9 | Environment diagnostics | **Partial** | `--doctor` covers most Linux setup failures and distinguishes `[bad]` from disconnected `[wait]`. | Add machine-readable diagnostic IDs, severity, remediation, and affected resource. Check KMonad compatibility, not just presence. Reuse bounded snapshot validation instead of a separate unbounded `cmd.Run` path. |
| 10 | Platform capabilities | **Missing** | README and implementation state that Linux/systemd is required. | Add a capability response. Unsupported features must be explicit and versioned so the GUI can adapt without OS checks. |
| 11 | Platform-specific device configuration | **Missing** | The parser extracts Linux `input (device-file "...")`, and the source file must already contain the OS path. | Introduce manager-owned device IDs and backend translation/rendering. A GUI-supplied profile must not contain `/dev/input` paths or choose a KMonad input backend. |
| 12 | Configuration ownership | **Partial** | External `.kbd` files with the currently supported Linux `device-file` input can be supervised; no GUI-specific format is required. | Add ownership metadata and APIs for managed configs without modifying or claiming external configs. Report `managed` versus `external`, support explicit adoption, and prevent visual-editor round trips from overwriting unsupported external syntax. |
| 13 | Multiple keyboards | **Implemented** | Each file has independent state and a KMonad process. Reconciliation changes only affected configs, while `rdev` identity prevents simultaneous claims and allows duplicate failover. | Move conflict checks to the stable-device model while retaining runtime node identity checks. Cover the same isolation guarantees through API-level tests. |
| 14 | Failure safety | **Partial** | Invalid updates preserve the running process; failures are isolated per config; launches retry with jittered backoff; immutable snapshots close validation races; status records failure context. | Persist a rollback-capable last-known-good revision and restore it when replacement startup or post-start confirmation fails. Emit explicit rejected, rollback, degraded, and recovered states/events. |
| 15 | Manager independence | **Implemented** | The systemd user service starts independently, supervises before/after CLI use, exposes CLI status/doctor commands, and recovers owned stale processes. | Keep all new APIs hosted by the long-running manager and retain useful CLI clients for headless operation. GUI exit must never imply manager shutdown. |

## Implementation task list

### P0 — Define the manager/GUI contract

- [x] **GUI-001: Specify a versioned local API** (all requirements).
  Cover protocol negotiation, full-state snapshots, device listing,
  identification sessions, candidate validation, apply/enable/disable/delete,
  diagnostics, capabilities, and event subscription. Keep raw PIDs and device
  paths out of required GUI workflows.
- [ ] **GUI-002: Define stable domain types and reason codes** (3, 4, 7-10,
  12). At minimum define `Device`, `Configuration`, `RuntimeState`,
  `ValidationResult`, `Diagnostic`, `Capability`, `Operation`, and `Event`.
  Human-readable messages may change; enum/code semantics must be stable.
- [ ] **GUI-003: Select and secure a per-user transport** (8, 15). Document
  endpoint discovery, peer authorization, file/socket permissions, request and
  payload limits, deadlines, cancellation, and compatibility policy. Do not
  expose the control plane on the metrics listener.
- [ ] **GUI-004: Keep state mutation single-owner** (5-8, 14). API handlers
  must submit commands to the reconciliation owner rather than mutate
  `manager.states` concurrently. Define idempotency and expected-revision
  behavior for retried apply requests. API/listener/client failure must be
  isolated from the systemd service's reconciliation and KMonad supervision.
- [x] **GUI-005: Separate service logic from `package main`** (all). The
  executable is a thin bootstrap that provides build metadata and process exit;
  `internal/manager` owns command dispatch, diagnostics, state, reconciliation,
  validated snapshots, and supervision. Its reconciliation goroutine is the
  sole mutator of configuration state; asynchronous process waits communicate
  results through channels.

### Completed foundation — platform boundary

- [x] **PLAT-000: Isolate current OS primitives behind `platform.System`.**
  Core manager code delegates device checks, runtime locking, process identity
  and signaling, cgroup lifecycle, user-service inspection, and service
  notification to build-tagged platform implementations. CI cross-compiles the
  core against the unsupported-platform implementation to prevent Linux APIs
  from leaking back into manager code.

### P1 — Device model and Linux discovery

- [ ] **DEV-001: Implement Linux keyboard enumeration** (1, 3, 13). Discover
  eligible physical keyboard event devices, filter non-keyboard/composite
  interfaces correctly, and expose display metadata such as name, vendor,
  product, serial, and connection state.
- [ ] **DEV-002: Define stable physical identity** (1, 3, 11, 13). Prefer
  serial-backed OS metadata, provide deterministic fallbacks and collision
  handling, and persist enough manager-owned metadata to represent known but
  disconnected devices. Keep stable physical identity distinct from the
  current node's `rdev`, which remains useful for live conflict detection.
- [ ] **DEV-003: Report detailed availability** (3, 7-9). Distinguish absent,
  inaccessible, unsupported, claimed/conflicting, and ready devices instead of
  returning one boolean.
- [ ] **DEV-004: Add identification sessions** (2, 3, 6). Support timeout,
  cancellation, concurrent-request rejection, and hotplug during a session.
  Determine whether KMonad's input grab prevents observation and, if so, pause
  and safely restore only the affected configuration. Never stop unrelated
  keyboards.
- [ ] **DEV-005: Test discovery without host hardware** (1-3). Put sysfs/udev
  and input-event access behind injectable interfaces; retain a small opt-in
  real-device test for Linux.

### P1 — Validation, platform translation, and safe apply

- [ ] **CFG-001: Add manager-owned input rendering** (4, 10, 11). Accept a
  manager device ID plus platform-neutral generated behavior and render the
  Linux KMonad input target inside the manager. Reject stale or ambiguous
  identity resolution with a structured result.
- [ ] **CFG-002: Build one side-effect-free validation pipeline** (4, 9-11).
  Accept candidate bytes/model without placing them in the watched directory;
  enforce size/time limits; resolve the device; check conflicts, permissions,
  dependencies, and capabilities; run KMonad dry-run on an immutable snapshot;
  and return errors, temporary conditions, and warnings separately.
- [ ] **CFG-003: Implement transactional apply** (5, 6, 14). Revalidate at
  apply time regardless of preview results, serialize against filesystem
  reconciliation, affect only the target config, persist atomically, and expose
  operation progress. Define when a newly started KMonad process is considered
  confirmed rather than merely spawned. Choose manager-owned writable storage
  and update the systemd sandbox narrowly; the current unit otherwise exposes
  only `%t` as writable while configured files normally live under the
  read-only protected home directory.
- [ ] **CFG-004: Add rollback after activation failure** (5, 14). Retain the
  previous durable bytes and launchable known-good revision until replacement
  confirmation. If start, cgroup attachment, or early health confirmation
  fails, restore/restart the prior revision and report whether rollback worked.
- [ ] **CFG-005: Add desired lifecycle operations** (5-7, 15). Support create,
  update, enable, disable, and delete without asking the GUI to rename or remove
  files. Preserve automatic reconnect for enabled configurations.
- [ ] **CFG-006: Preserve external configurations** (12). Store GUI ownership,
  model version, stable device ID, and revision in sidecar/manager metadata.
  Inventory external `.kbd` files as read-only to the visual editor unless the
  user explicitly adopts them. Detect external edits to managed files and avoid
  silent overwrite or lossy round trips.

### P1 — Public state, operations, and events

- [ ] **STATE-001: Split desired, active, and operation state** (5, 7, 14).
  Represent the active known-good revision independently from a pending or
  rejected candidate. Include stable config/device IDs, enabled state,
  availability, runtime health, retry data, and structured reasons.
- [ ] **STATE-002: Serve authoritative snapshots** (1, 3, 7, 12, 13). Include
  connected-unconfigured devices, known-disconnected devices, managed/external
  configurations, active operations, manager health, and a monotonic state
  revision. Keep `--status=json` as a CLI view or compatibility adapter.
- [ ] **EVENT-001: Add an ordered event stream** (8). Publish device,
  availability, validation, apply, rollback, configuration, process,
  dependency, and recovery transitions with event ID, state revision, time,
  resource ID, type, and reason code.
- [ ] **EVENT-002: Define resynchronization semantics** (7, 8). A reconnecting
  GUI must be able to resume after an event ID or fetch a new snapshot when the
  retained history has expired. Slow clients must not block supervision.
- [ ] **STATE-003: Keep logs and metrics operational** (6-9, 15). Derive stable
  events and public state from manager transitions, not by parsing log text;
  retain structured logs and Prometheus metrics for operators.

### P2 — Diagnostics, capabilities, and platform boundaries

- [ ] **DIAG-001: Return structured diagnostics** (4, 9). Give each check a
  stable ID, severity (`error`, `temporary`, `warning`, `ok`), summary,
  remediation, and affected resource. Preserve human-readable `--doctor` by
  rendering the same results.
- [ ] **DIAG-002: Complete dependency checks** (8, 9). Add KMonad version and
  compatibility reporting and detect runtime dependency/permission regressions
  that should change status or emit events.
- [ ] **CAP-001: Expose capabilities** (2, 10, 11, 13). Report platform,
  backend, manager/API/KMonad versions, discovery, identification, per-device
  mapping, multiple instances, hotplug recovery, supported input target, and
  any feature limitations.
- [ ] **PLAT-001: Introduce platform backend interfaces** (1-3, 9-11). Isolate
  device enumeration/identity/events, input rendering, diagnostics, process
  primitives, and service integration from the supervisor. Move current
  `/proc`, pidfd, `syscall.Stat_t`, `/dev/uinput`, and systemd assumptions into
  the Linux implementation.
- [ ] **PLAT-002: Advertise Linux truthfully first** (10). A Linux-only first
  release is acceptable; unsupported capabilities must be false rather than
  simulated in the GUI.
- [ ] **PLAT-003: Add macOS/Windows only as separate backends** (10, 11, 13).
  Do not put IOKit, global-hook, or service-manager branches into GUI code.
  Each backend needs its own discovery, identity, rendering, supervision,
  diagnostics, capability, and integration tests before being advertised.

### P2 — Safety, compatibility, and test coverage

- [ ] **SAFE-001: Threat-model the control API** (4, 5, 9, 15). Treat apply as
  privileged input: authenticate the local user, bound all data and child
  processes, reject unsafe config storage, avoid following attacker-controlled
  paths, and redact platform locators where clients do not need them.
- [ ] **TEST-001: Add API contract tests** (all). Cover schema/version
  compatibility, reason codes, cancellation, authorization, idempotent retries,
  stale revisions, malformed requests, and slow/disconnected subscribers.
- [ ] **TEST-002: Add device lifecycle tests** (1-3, 8, 13). Cover duplicate
  models, missing serials, composite devices, reconnect with a changed event
  node, known-disconnected inventory, permission loss/recovery, and identify
  cancellation.
- [ ] **TEST-003: Add transactional apply tests** (4-6, 12, 14). Prove preview
  validation has no side effects, apply validates again, unrelated keyboards
  keep running, external configs remain untouched, startup failure rolls back,
  and manager/GUI crashes at each transaction phase recover safely.
- [ ] **TEST-004: Add snapshot/event consistency tests** (7, 8). Prove event
  ordering, no missed terminal operation state, reconnect/resync behavior, and
  agreement between an event-reduced view and a fresh snapshot.
- [ ] **COMPAT-001: Preserve headless workflows** (12, 15). Continue scanning
  external `.kbd` files and retain status, doctor, completion, installer, and
  service workflows. Document any status schema migration before changing the
  existing JSON output.

## Linux GUI-ready milestone

The first GUI-facing release is complete when all of the following are true:

- [ ] A client can negotiate the API and capabilities without checking the OS.
- [ ] It can list connected, configured, and known-disconnected keyboards by
  manager-owned identity and identify one through a bounded keypress session.
- [ ] It can preview validation with structured results and no runtime effect.
- [ ] It can create or update one managed configuration through a final
  revalidation and rollback-capable apply transaction.
- [ ] It can distinguish active, pending, rejected, waiting, conflicting,
  failed, disabled, and recovered states with stable reasons.
- [ ] It can consume ordered events and resynchronize from an authoritative
  snapshot.
- [ ] It can display structured diagnostics and remediation supplied by the
  manager.
- [ ] External `.kbd` files still run and are never rewritten merely because a
  GUI connected.
- [ ] Closing or crashing the GUI has no effect on running mappings.
- [ ] Existing multi-keyboard, hotplug, failure-isolation, and headless tests
  continue to pass, with new contract and rollback tests in CI.
- [ ] The systemd service continues to build, start, supervise, recover, and
  stop external configurations with no GUI, no API client, and an unavailable
  API listener.
