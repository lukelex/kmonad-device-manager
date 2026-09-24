# KeyboarDeer manager integration contract

The Linux manager implementation first contains all three GUI prerequisites at
repository revision **`8190ee5`** (the content/export commit, after validation
`51b59e1` and idempotent mutation `980ca08`). This is a repository revision,
not a released package version. Use `manager.get` capabilities at runtime;
`v1.0.0` release artifacts do not contain these additions.

## Required API v1 capabilities

- `candidate_validation`: model previews include `candidate_digest` and only
  trustworthy `submitted_behavior` assignment locations on rejected results.
- `managed_configurations` and `external_configuration_adoption`: create,
  update, apply, enable/disable, delete, and adopt require a durable
  `idempotency_key` (1–128 bytes). Reuse the key and original parameters after
  a lost response; use `operation.get` and `snapshot.get` to inspect outcomes.
- `configuration_content_read`: read external UTF-8 source with
  `configuration.content.get` and the current `content_revision` from inventory.
  Its `sha256:` digest binds the returned bytes; this does not authorize edit
  or import.
- `configuration_export`: request `manager_rendered_kbd` from
  `configuration.export` for a manager-owned, device-bound `.kbd` artifact.
  Never interpret it as a portable GUI profile.
- `event_stream`, `device_discovery`, and `device_identification`: keep using
  existing snapshot/cursor resynchronization and bounded identify semantics.

The CLI equivalents are documented in the [Command reference](Command-Reference).
All content stays behind the same-user Unix socket. The manager still owns
device selection, input/output rendering, validation, supervision, and durable
configuration state independently of a GUI session.

## Acceptance status

Containerized race, vet, fake-KMonad lifecycle, installer, static-build,
versioned JSON Lines fixture, and socket-level tests verify the wire and
restart/disconnect contracts. Before releasing these APIs for KeyboarDeer,
run the two-physical-keyboard Linux/evdev acceptance scenario in
[`TODO.md`](https://github.com/lukelex/kmonad-device-manager/blob/main/TODO.md):
independent identify, hotplug, one-device apply, lost-response recovery,
rollback, and continued headless systemd supervision. Containerized fake-device
tests cannot substitute for that physical-keyboard check.
