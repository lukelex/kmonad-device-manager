# Changelog

All notable changes to KMonad Device Manager are documented here.

## [Unreleased]

## [1.2.0] - 2026-09-24

### Added

- Add the read-only, capability-gated `device.inputscan.get` key-capability
  scan and the bounded `device.inputscan.probe` single-key observation session,
  exposed as `kmonad-device-manager inputscan {get,probe,status,cancel}`.
  Scans translate a device's evdev `EV_KEY` capability array into the versioned
  `kmonad-v1` KMonad token vocabulary with a deterministic digest and a
  per-device evidence generation; probes wait for one named token's keypress
  and record only whether it arrived. Both respect per-keyboard isolation,
  never infer a layout or geometry, and never change a mapping.
- Add JSON Lines contract fixtures and replay tests for `device.inputscan.get`
  covering exact, superset, subset, and partial key sets, vendor keys counted
  as unmapped, stale generation and digest after hotplug, a manager that does
  not advertise `device_input_scan`, and the `unsupported_capability` error.
- Add the opt-in `tests/acceptance/inputscan-test04.sh` real-device acceptance
  run and its checklist, covering board agreement, scan/identify agreement,
  the ISO/ANSI probe split, and hotplug invalidation.
- Add the macOS backend scope, including the KMonad per-device isolation and
  privileged-broker prerequisites for a future supported release.

### Changed

- Split non-Linux platform descriptors so macOS and Windows are identified
  explicitly while remaining unavailable until their complete native backends
  are implemented.
- Permit API compatibility breaks during development while preserving the
  existing systemd service supervision behavior.

## [1.1.0] - 2026-09-24

### Added

- Add `candidate_digest` to model validation previews and conservative
  `submitted_behavior` diagnostic locations for rejected KMonad assignments.
- Add durable idempotency keys for managed create, update, apply, lifecycle,
  delete, and external-adoption mutations, including replay after client
  disconnect or manager restart.
- Add bounded same-user reads of external `.kbd` text at a content-derived
  revision, plus integrity-checked `manager_rendered_kbd` export of managed
  immutable revisions, without exposing manager paths.
- Add JSON Lines integration fixtures for diagnostic locations, idempotent
  replay/restart, external content reads, and managed exports.
- Run the installer source-build integration test as an unprivileged Docker
  user and cover additional Linux process-launch error paths.

### Changed

- Document implemented Linux API v1 resource methods, CLI equivalents, and
  runtime capability checks.
- Gate tag-triggered release publication on complete container verification and
  enforce tag, Arch package, changelog, release-note, and binary-version
  consistency before building release artifacts.

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
