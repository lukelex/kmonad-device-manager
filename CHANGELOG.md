# Changelog

All notable changes to KMonad Device Manager are documented here.

## [Unreleased]

### Added

- Persist bounded idempotency records for configuration mutations so repeated
  requests and lost responses recover the same operation across restarts.
- Run the installer source-build integration test as an unprivileged Docker
  user and cover additional Linux process-launch error paths.
- Add submitted-behavior diagnostic locations and candidate digests to model
  validation previews, including a JSON Lines integration fixture for mapped,
  unmapped, and blocked results.
- Add initial KeyboarDeer GUI branding concepts: a deer-at-the-keyboard SVG
  logo, compact app-icon study, and visual direction notes.
- Add a cross-referenced implementation plan for a future GUI-facing manager
  API, device discovery, transactional apply, events, and platform backends.
- Add the macOS backend scope, including the KMonad per-device isolation and
  privileged-broker prerequisites for a future supported release.

### Changed

- Document implemented Linux API v1 resource methods and runtime capabilities.
- Split non-Linux platform descriptors so macOS and Windows are identified
  explicitly while remaining unavailable until their complete native backends
  are implemented.
- Permit GUI/API compatibility breaks during development while preserving the
  existing systemd service supervision behavior.

### Fixed

- Stabilize managed activation and rollback confirmation across child-process
  exec scheduling and short-lived `/proc` observation gaps.

## [1.0.0] - 2026-09-24

### Added

- Add a versioned same-user local API v1, JSON-capable CLI clients, authoritative
  snapshots, ordered event streaming, and structured diagnostics.
- Add Linux keyboard discovery, stable identity handling, retained disconnected
  device records, manager-output device roles, and bounded keypress
  identification sessions.
- Add transactional managed configuration create, update, enable, disable,
  delete, validation, activation confirmation, rollback, and safe adoption of
  external `.kbd` configurations.
- Add immutable validation and launch snapshots, known-good update recovery,
  per-configuration runtime state, and configuration revision reporting.
- Add `manager get`, capability/limitation reporting, KMonad compatibility
  checks, public manager health, Prometheus transition metrics, and public
  diagnostics through the API and `--doctor --json`.
- Add bounded command, request, event, configuration, and process lifecycle
  controls, with complete CLI-to-manager integration coverage.
- Move KMonad child-process construction and setup behind the platform backend.

### Changed

- Keep the Linux systemd service as the independent owner of external `.kbd`
  supervision, reconciliation, recovery, and per-keyboard isolation even when
  API clients are absent or fail.
- Serialize API mutations through a bounded reconciliation-owned command
  mailbox, preventing control-plane clients from mutating supervision state
  concurrently.
- Include vendor and product with serial-backed Linux device identity, falling
  back to topology when duplicate serial identities are discovered.
- Reject management operations that select manager-created virtual output
  devices, while preserving their classification across disconnection.
- Replace the legacy `--status=json` spelling with `--status --json` and expand
  the CLI help, man page, and completions with complete JSON-aware command
  documentation.
- Isolate Linux device, process, cgroup, systemd, runtime-lock, and user-group
  primitives behind the platform boundary.

### Fixed

- Render complete Linux manager-owned KMonad `defcfg` headers, including the
  stable uinput output, before dry-run validation and apply.

### Security

- Reject unsafe configuration paths and untrusted manager-created virtual
  outputs before identification, validation, apply, or lifecycle actions.
- Preserve immutable validated launch snapshots and prevent silent overwrite of
  externally modified manager-owned revisions.

## [0.6.0] - 2026-09-21

### Added

- Launch validated KMonad configurations from immutable snapshots.
- Add bounded manager lifecycle soak testing to the integration harness.

### Fixed

- Ensure validation and launch use the same configuration contents.
- Prevent dry-run processes from outliving the manager.

## [0.5.0] - 2026-09-21

### Added

- Add durable status persistence and manager health metrics.
- Add bounded retry backoff jitter and shared stop deadlines.
- Add process identity checks, pidfd signaling, and descendant cleanup.
- Add cgroup lifecycle cleanup and configuration capacity limits.
- Add recovery coverage for filesystem watches, status, cgroups, and process
  signaling.

### Changed

- Update the Go toolchain to 1.27 and refresh local and CI dependencies.
- Tie the systemd watchdog to manager reconciliation progress.

## [0.4.1] - 2026-09-21

### Changed

- Build and test with Go 1.25.

## [0.4.0] - 2026-09-21

### Added

- Add an opt-in user systemd lifecycle test.
- Add delegated cgroup integration coverage.
- Add end-to-end source installer coverage.
- Attest and verify reproducible release artifacts.

### Changed

- Make development builds clearly report their unversioned status.
- Require explicit opt-in for remote metrics binding.
- Make status output machine-readable and identity-safe.
- Split the manager runtime into focused source files.

## [0.3.6] - 2026-09-21

### Changed

- Format status output as a process table.

## [0.3.5] - 2026-09-21

### Fixed

- Allow the packaged manager to write required runtime state.

## [0.3.4] - 2026-09-21

### Added

- Add configuration rollback and per-process cgroups.

## [0.3.3] - 2026-09-21

### Added

- Add man-page installation, fault-injection coverage, and systemd watchdog
  support.

## [0.3.2] - 2026-09-21

### Added

- Add watchdog ownership persistence and status reporting.
- Expand coverage for status health and process ownership checks.

## [0.3.1] - 2026-09-21

### Added

- Add event watching, observability, resource limits, stale-process recovery,
  reproducible multi-architecture releases, and coverage enforcement.

### Fixed

- Reject insecure configuration paths and parse device declarations safely.

## [0.3.0] - 2026-09-21

### Added

- Rewrite the manager in Go.

## [0.2.0] - 2026-09-21

### Changed

- Harden manager resilience.

## [0.1.0]

Initial release.
