# GUI Integration Gap Analysis and Task Plan

Assessment date: 2026-09-24 (manager `v1.2.0`, commit `6fd18e8`)

## Goal and scope

The GUI must be a client of `kmonad-device-manager`, not the owner of the
manager process. The GUI owns the editable keyboard-behavior model; the manager
owns device identity, platform translation, validation, activation, KMonad
processes, runtime state, and diagnostics.

The CLI remains a first-class manager client and automation interface. New API
or GUI capabilities must have an equivalent CLI workflow with meaningful JSON
output; interactive or streaming capabilities must provide CLI start, status,
and cancel operations rather than becoming GUI-only.

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

The manager retains its headless configuration-directory supervision workflow
and also provides API v1 over a same-user Unix socket. The API exposes device
inventory and identification, candidate validation, managed configuration
lifecycle, revision-checked external content reads, managed artifact export,
authoritative snapshots, structured diagnostics, operations, and ordered
events. The CLI remains a supported interface; API availability is reported by
capability rather than inferred by clients. Core state and service logic live
in `internal/manager`; OS primitives are delegated to `internal/platform`.

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
| 1 | Device discovery | **Implemented** | Linux discovery enumerates keyboard-capable devices; the manager persists a device registry, including known-disconnected devices. | Topology-based identity is confidence-limited and may change when hardware topology changes; physical acceptance remains outstanding. |
| 2 | Keyboard identification | **Implemented** | Bounded identify operations support timeout, cancellation, hotplug, and pausing only the selected device's managed process. | Complete two-physical-keyboard release acceptance. |
| 3 | Device availability | **Implemented** | API, CLI, and snapshots report connected, disconnected, inaccessible, unsupported, conflicting, and manager-output roles. | Complete permission-loss/recovery and reconnect hardware acceptance. |
| 4 | Candidate validation | **Implemented** | `validation.preview` runs the bounded reusable validation pipeline on immutable snapshots and returns structured results and conservative diagnostic attribution. | Hardware acceptance is still needed for real evdev behavior. |
| 5 | Applying configuration | **Implemented** | Managed apply/create/update/lifecycle operations use expected revisions, durable idempotency, final validation, persistence, activation confirmation, and rollback. | Complete lost-response and rollback scenarios against the installed service with physical devices. |
| 6 | Process supervision | **Implemented** | Headless reconciliation, per-configuration supervision, recovery, and API operations coexist; clients do not control PIDs. | Retain the physical hardware acceptance gate. |
| 7 | Runtime status | **Implemented** | Versioned snapshots expose desired/active revisions, runtime health, operations, diagnostics, and state revision; CLI status remains available. | Verify state against physical hotplug and apply transitions. |
| 8 | Runtime changes and events | **Implemented** | Ordered event replay and live subscriptions use server/event cursors, bounded queues, and resync semantics. | Complete physical reconnect and client-restart acceptance. |
| 9 | Environment diagnostics | **Implemented** | Structured diagnostics include stable IDs, severity, remediation, resource, KMonad compatibility, and runtime dependency/permission findings; `--doctor` renders them. | Hardware acceptance for runtime permission changes remains. |
| 10 | Platform capabilities | **Implemented** | `manager.get` reports truthful backend capabilities and limitations; non-Linux backends remain unavailable. | macOS/Windows support is separate future work. |
| 11 | Platform-specific device configuration | **Implemented** | The GUI model uses opaque manager device IDs; the manager resolves and renders the platform-specific KMonad input target. | Linux is the only available backend. |
| 12 | Configuration ownership | **Implemented** | Managed and external configurations are distinguished; external configurations are read-only unless losslessly adopted, and content reads are revision/digest checked. | Keep external-file preservation in release acceptance. |
| 13 | Multiple keyboards | **Implemented** | Device conflicts and managed operations are isolated per keyboard; API and unit/integration coverage exercise independent resources. | Complete two-device physical acceptance. |
| 14 | Failure safety | **Implemented** | Immutable validated revisions, health-confirmed activation, rollback, durable idempotency, and structured failure state are implemented. | Verify rollback and service continuity on physical hardware. |
| 15 | Manager independence | **Implemented** | The systemd service owns lifecycle and remains operational without the GUI/API; API failures are isolated from reconciliation. | Complete close/crash-during-apply hardware acceptance. |

## Implementation task list

### P0 — Define the manager/GUI contract

- [x] **GUI-001: Specify a versioned local API** (all requirements).
  Cover protocol negotiation, full-state snapshots, device listing,
  identification sessions, candidate validation, apply/enable/disable/delete,
  diagnostics, capabilities, and event subscription. Keep raw PIDs and device
  paths out of required GUI workflows.
- [x] **GUI-002: Define stable domain types and reason codes** (3, 4, 7-10,
  12). `internal/manager/domain.go` and API v1 define `Device`,
  `Configuration`, `RuntimeState`, `ValidationResult`, `Diagnostic`,
  `Capability`, `Operation`, `Event`, opaque resource IDs, stable enum values,
  and a versioned lower-snake-case reason-code vocabulary. Human-readable
  messages remain display-only.
- [x] **GUI-003: Select and secure a per-user transport** (8, 15). The manager
  owns a same-user Unix socket with a manager-private `0700` directory and
  `0600` socket, verifies Linux peer credentials before accepting a connection,
  bounds frames, clients, in-flight requests, and deadlines, and closes all
  clients during manager shutdown. Transport, peer, and request failures are
  isolated from reconciliation. `session.hello` and Linux resource methods are
  implemented; clients inspect `manager.get` for runtime availability.
  Future security follow-ups: add a separately designed remote/cross-user
  transport with mutual TLS, evaluate a dedicated service-user boundary when
  same-user clients are not mutually trusted, and reassess peer authorization
  plus path/secret disclosure in API responses and logs as the contract grows.
- [x] **GUI-004: Keep state mutation single-owner** (5-8, 14). API resource
  handlers submit bounded, deadline-aware commands to the reconciliation owner
  rather than mutate `manager.states` concurrently. A full queue affects only
  the caller; the owner skips expired queued requests and commands must honor
  their context before a late mutation.
  Managed mutations now require durable idempotency keys at the API boundary;
  expected-revision checks remain enforced. API/listener/client failure is isolated
  from systemd service reconciliation and KMonad supervision.
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

- [x] **DEV-001: Implement Linux keyboard enumeration** (1, 3, 13). Discover
  connected keyboard-capable event interfaces from Linux sysfs, require normal
  keyboard key capabilities to exclude pointer-only devices, and expose name,
  vendor, product, serial when available, and connection state through
  `device.list` and `kmonad-device-manager devices`. Persistent disconnected
  identity and richer composite filtering remain DEV-002/DEV-003 work.
- [x] **DEV-002: Define stable physical identity** (1, 3, 11, 13). Prefer
  serial-backed OS metadata, provide deterministic fallbacks and collision
  handling, and persist enough manager-owned metadata to represent known but
  disconnected devices. Keep stable physical identity distinct from the
  current node's `rdev`, which remains useful for live conflict detection.
  Serial identities include vendor/product and duplicate serials fall back to
  topology. A manager-owned device registry retains disconnected identities
  across service restarts. Identity stability is exposed so clients can
  communicate topology-based confidence; live `rdev` remains separate for
  conflict detection.
- [x] **DEV-003: Report detailed availability** (3, 7-9). Distinguish absent,
  inaccessible, unsupported, claimed/conflicting, and ready devices through
  platform-isolated checks, API/CLI device records, and status snapshots instead
  of returning one boolean.
- [x] **DEV-004: Add identification sessions** (2, 3, 6). Support timeout,
  cancellation, concurrent-request rejection, and hotplug during a session.
  The manager pauses and restores only configurations bound to the selected
  device while observing a keypress, so unrelated keyboards continue running.
- [x] **DEV-005: Test discovery without host hardware** (1-3). Put Linux sysfs
  discovery and evdev input-event access behind an injectable platform backend,
  with fixture coverage that requires no host keyboard or `/dev/input` access.
  Retain `KMONAD_TEST_REAL_INPUT=1 go test ./internal/platform` as a small
  opt-in real-device smoke test.

### P1 — Validation, platform translation, and safe apply

- [x] **CFG-001: Add manager-owned input rendering** (4, 10, 11). Accept a
  manager device ID plus platform-neutral generated behavior and render the
  Linux KMonad input target inside the manager. Stale, unavailable, conflicting,
  and ambiguous identity resolution return structured validation results. This
  rendering foundation is side-effect-free; candidate validation and lifecycle
  API operations remain CFG-002/CFG-005 work.
- [x] **CFG-002: Build one side-effect-free validation pipeline** (4, 9-11).
  Accept candidate bytes/model without placing them in the watched directory;
  enforce size/time limits; resolve the device; check conflicts, permissions,
  dependencies, and capabilities; run KMonad dry-run on an immutable snapshot;
  and return errors and temporary conditions as structured validation results.
  Preview dry-runs execute outside the reconciliation owner so slow candidates
  cannot delay configured keyboard supervision.
- [x] **CFG-003: Implement transactional apply** (5, 6, 14). Revalidate at
  apply time regardless of preview results, serialize against filesystem
  reconciliation, affect only the target config, persist atomically, and expose
  operation progress. Define when a newly started KMonad process is considered
  confirmed rather than merely spawned. Choose manager-owned writable storage
  and update the systemd sandbox narrowly; the current unit otherwise exposes
  only `%t` as writable while configured files normally live under the
  read-only protected home directory. Managed revisions now use a narrowly
  writable systemd `StateDirectory`; apply validates outside the owner, commits
  immutable revision files and metadata atomically on the owner, and confirms
  activation after process ownership and health checks.
- [x] **CFG-004: Add rollback after activation failure** (5, 14). Retain the
  previous durable bytes and launchable known-good revision until replacement
  confirmation. If start, cgroup attachment, or early health confirmation
  fails, restore/restart the prior revision and report whether rollback worked.
  Failed replacements now restore the previous metadata atomically, stop the
  unconfirmed replacement, restart the previous immutable revision, and return
  `rolled_back` with `runtime_rollback_succeeded` only after recovery passes
  the process ownership and health check.
- [x] **CFG-005: Add desired lifecycle operations** (5-7, 15). Support create,
  update, enable, disable, and delete without asking the GUI to rename or remove
  files. Preserve automatic reconnect for enabled configurations. Explicit
  create/update, enable/disable, and delete API and CLI operations now enforce
  expected revisions; enable returns the mapping to normal reconciliation and
  delete removes only manager-owned revisions after stopping its process.
- [x] **CFG-006: Preserve external configurations** (12). Store GUI ownership,
  model version, stable device ID, and revision in sidecar/manager metadata.
  Inventory external `.kbd` files as read-only to the visual editor unless the
  user explicitly adopts them. Detect external edits to managed files and avoid
  silent overwrite or lossy round trips. `configuration.list`/`config list`
  now inventories external configurations as read-only records with private
  sidecar metadata; manager-owned metadata records ownership, model, device,
  and revision. Altered immutable managed bytes are surfaced as
   `configuration_changed` and cannot be overwritten silently. Explicit
   adoption accepts only a canonical, losslessly representable device-file
   input form; it preserves the external source and hands off its unchanged
   digest only after managed activation can begin, reverting on failure.

### P1 — Public state, operations, and events

- [x] **STATE-001: Split desired, active, and operation state** (5, 7, 14).
  Represent the active known-good revision independently from a pending or
  rejected candidate. Include stable config/device IDs, enabled state,
  availability, runtime health, retry data, and structured reasons. Managed
  metadata now persists the last health-confirmed active content revision
  separately from desired model/lifecycle revision; `configuration.list`/
  `config list --json` include both revisions, runtime retry state, and the
  most recently retained operation for each managed configuration.
- [x] **STATE-002: Serve authoritative snapshots** (1, 3, 7, 12, 13). Include
  connected-unconfigured devices, known-disconnected devices, managed/external
  configurations, active operations, manager health, and a monotonic state
  revision. Keep `--status --json` as a CLI view or compatibility adapter.
  `snapshot.get` and `snapshot --json` now return devices, configurations,
  retained operations, public health counters, and a monotonic state revision
  through the reconciliation owner; status-file commands remain unchanged.
- [x] **EVENT-001: Add an ordered event stream** (8). Publish device,
  availability, validation, apply, rollback, configuration, process,
  dependency, and recovery transitions with event ID, state revision, time,
  resource ID, type, and reason code. `events.subscribe` now replays retained
  ordered events and follows live owner-published transitions through bounded
  per-subscriber queues; lagging clients receive `manager.resync_required`
  without blocking reconciliation. The `events subscribe` CLI mirrors the
  stream as Event JSON Lines.
- [x] **EVENT-002: Define resynchronization semantics** (7, 8). A reconnecting
  GUI must be able to resume after an event ID or fetch a new snapshot when the
  retained history has expired. Slow clients must not block supervision.
  Snapshots now expose an `event_cursor` with manager server ID, latest event
  ID, and state revision. Resuming with that cursor either replays strictly
  newer retained events or sends `manager_resync_required` on server restart,
  expired/future history, or subscriber overflow.
- [x] **STATE-003: Keep logs and metrics operational** (6-9, 15). Derive stable
  events and public state from manager transitions, not by parsing log text;
  retain structured logs and Prometheus metrics for operators. The transition
  publisher now writes sanitized `manager_transition` JSON logs and increments
  stable per-event Prometheus counters alongside its retained API event stream;
  the metrics endpoint also exposes the latest public state revision.

### P2 — Diagnostics, capabilities, and platform boundaries

- [x] **DIAG-001: Return structured diagnostics** (4, 9). Give each check a
  stable ID, severity (`error`, `temporary`, `warning`, `ok`), summary,
  remediation, and affected resource. Preserve human-readable `--doctor` by
  rendering the same results. Snapshots now return owner-derived manager,
  device, and configuration diagnostics and emit `diagnostic.changed` when a
  diagnostic changes or resolves; `--doctor` now renders and returns the same
  Diagnostic schema.
- [x] **DIAG-002: Complete dependency checks** (8, 9). Add KMonad version and
  compatibility reporting and detect runtime dependency/permission regressions
  that should change status or emit events. The service performs a bounded,
  platform-owned KMonad version probe at startup, reports compatibility in
  `manager.get` and `manager.kmonad` diagnostics, and detects executable
  disappearance during reconciliation; existing inaccessible-device diagnostics
  cover runtime keyboard permission regressions.
- [x] **CAP-001: Expose capabilities** (2, 10, 11, 13). Report platform,
  backend, manager/API/KMonad versions, discovery, identification, per-device
  mapping, multiple instances, hotplug recovery, supported input target, and
  any feature limitations. `manager.get` now reports platform/backend versions,
  complete truthful capability availability, and stable limits for Linux-only,
  device-file input, external read-only, and local same-user API boundaries.
- [x] **PLAT-001: Introduce platform backend interfaces** (1-3, 9-11). Isolate
  device enumeration/identity/events, input rendering, diagnostics, process
  primitives, and service integration from the supervisor. Move current
  `/proc`, pidfd, `syscall.Stat_t`, `/dev/uinput`, and systemd assumptions into
  the Linux implementation. The manager core now receives only platform-started
  child processes with PID/Wait operations; Linux owns `exec.Cmd` construction,
  process-group setup, pidfds, cgroups, discovery, input rendering, and service
  integration. Non-Linux builds retain an unavailable backend implementation.
- [x] **PLAT-002: Advertise Linux truthfully first** (10). A Linux-only first
  release is acceptable; unsupported capabilities must be false rather than
  simulated in the GUI. The unavailable backend reports false capabilities and
  a `platform_unsupported` doctor diagnostic; service and platform-dependent
  CLI operations return the structured `unsupported_platform` error.
- [ ] **PLAT-003: Add macOS/Windows only as separate backends** (10, 11, 13).
  Do not put IOKit, global-hook, or service-manager branches into GUI code.
  Each backend needs its own discovery, identity, rendering, supervision,
  diagnostics, capability, and integration tests before being advertised. The
  initial scaffolding now identifies Darwin and Windows through separate,
  explicitly unavailable platform descriptors and verifies Darwin, Windows, and
  generic non-Linux target builds; native capabilities remain false pending
  those complete backends. The accepted macOS architecture, packaging contract,
  and KMonad per-device isolation prerequisite are specified in
  [macOS Backend Plan](macOS-Backend-Plan.md).

### P2 — Safety, compatibility, and test coverage

- [x] **SAFE-001: Threat-model the control API** (4, 5, 9, 15). See the
  [API security and trust model](Manager-API-v1#security-and-trust-model).
  API v1 is a same-effective-UID local control plane, not a boundary between
  mutually untrusted processes owned by the same user. Transport permissions
  and peer credentials, bounded requests, protected manager storage, immutable
  validation snapshots, and omission of host locators from GUI responses
  mitigate cross-user access, resource exhaustion, and path substitution. The
  deployment assumption and residual same-user risks are documented explicitly.
- [ ] **TEST-001: Complete API contract test matrix** (all). Existing transport,
  method, validation, cancellation, idempotency, stale-revision, content, and
  event tests cover the implemented Linux API. Remaining work is a systematic
  schema/reason-code compatibility fixture and explicit slow-subscriber,
  authorization-boundary, and unknown-field forward-compatibility cases; retain
  the physical peer/keyboard checks as separate acceptance coverage.
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
