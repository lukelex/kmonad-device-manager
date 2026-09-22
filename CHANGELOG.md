# Changelog

All notable changes to KMonad Device Manager are documented here.

## [Unreleased]

### Added

- Define the versioned, same-user local manager API v1 contract for future GUI
  clients.
- Define that GUI/API work is optional and cannot affect independent systemd
  service supervision or recovery.
- Add a cross-referenced implementation plan for a future GUI-facing manager
  API, device discovery, transactional apply, events, and platform backends.
- Add meaningful JSON output to every public manager command, including
  structured diagnostics, help, completion data, and command errors.
- Add an indexed GitHub Wiki command reference and synchronization workflow.
- Add stable API v1 manager-domain types, enums, and reason codes for future
  GUI clients.
- Add a same-user Unix-socket API transport with peer authorization, bounded
  JSON Lines requests, session negotiation, and isolated client failures.
- Add connected Linux keyboard discovery through the API and `devices` CLI
  command, with JSON metadata and no device-path exposure.
- Add detailed device availability and stable reason codes for disconnected,
  inaccessible, unsupported, and conflicting keyboard interfaces.
- Add bounded keyboard keypress identification sessions through the local API
  and CLI, pausing and restoring only the selected keyboard's KMonad process.

### Changed

- Include vendor and product with serial-backed device identity, falling back
  to topology when duplicate serial identities are discovered.
- Retain known disconnected keyboard records in a manager-owned runtime device
  registry across service restart.
- Route API resource requests through a bounded, serialized manager command
  mailbox so control-plane clients cannot mutate supervision state concurrently.
- Move manager command handling, service lifecycle, diagnostics, status, and
  supervision into `internal/manager`, leaving the executable as a thin
  bootstrap and serializing configuration-state mutation in reconciliation.
- Isolate Linux device, process, cgroup, systemd, runtime-lock, and user-group
  primitives behind a shared platform interface with non-Linux build support.
- Permit GUI/API compatibility breaks during development while preserving the
  existing systemd service supervision behavior.
- Clarify automatic `.kbd` change detection, validation, and reload behavior,
  and organize the README from lifecycle basics to advanced settings.
- Expand built-in help and the man page with every command, argument, option,
  JSON contract, example, and exit status.

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
