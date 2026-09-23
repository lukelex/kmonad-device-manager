# Command reference

`kmonad-device-manager` is both a long-running per-user service and a set of
one-shot inspection commands. The systemd user unit uses service mode; the
other commands are safe clients for diagnostics, status, completions, version
metadata, and help.

Every public invocation accepts `--json`. The option may appear before or after
the command selector. One-shot commands emit one JSON document. Service mode
emits JSON Lines to standard error so log consumers can process each event as
it occurs. Errors requested as JSON are objects with `error.code` and
`error.message` fields.

## Command index

| Command | Invocation | Purpose |
|---|---|---|
| [service](#service) | `kmonad-device-manager [--json]` | Run the foreground manager service. |
| [doctor](#doctor) | `kmonad-device-manager --doctor [--json]` | Check system readiness and configured keyboards. |
| [status](#status) | `kmonad-device-manager --status [--json]` | Read the authoritative manager status snapshot. |
| [ps](#ps) | `kmonad-device-manager ps [--json]` | Use the process-list-style status alias. |
| [devices](#devices) | `kmonad-device-manager devices [--json]` | List known keyboard-capable input interfaces. |
| [manager](#manager) | `kmonad-device-manager manager get [--json]` | Read public manager metadata and capabilities. |
| [snapshot](#snapshot) | `kmonad-device-manager snapshot [--json]` | Read authoritative public manager state. |
| [events](#events) | `kmonad-device-manager events subscribe [--after EVENT_ID] [--server SERVER_ID] [--json]` | Stream ordered public manager events. |
| [identify](#identify) | `kmonad-device-manager identify {start DEVICE_ID [--timeout SECONDS]\|status OPERATION_ID\|cancel OPERATION_ID} [--json]` | Run, inspect, or cancel a keypress identification session. |
| [validate](#validate) | `kmonad-device-manager validate {model MODEL_FILE\|file KBD_FILE} [--json]` | Preview one candidate without applying it. |
| [apply](#apply) | `kmonad-device-manager apply MODEL_FILE [--name NAME] [--id CONFIGURATION_ID --revision REVISION] [--json]` | Transactionally persist and activate one managed configuration. |
| [config](#config) | `kmonad-device-manager config { list | create MODEL_FILE --name NAME | update CONFIGURATION_ID REVISION MODEL_FILE [--name NAME] | enable CONFIGURATION_ID REVISION | disable CONFIGURATION_ID REVISION | delete CONFIGURATION_ID REVISION | adopt EXTERNAL_CONFIGURATION_ID [--name NAME] } [--json]` | List or manage configurations. |
| [completion](#completion) | `kmonad-device-manager --completion SHELL [--json]` | Print an embedded shell-completion definition. |
| [version](#version) | `kmonad-device-manager --version [--json]` | Show build version metadata. |
| [help](#help) | `kmonad-device-manager {-h\|--help} [--json]` | Show the complete in-program command reference. |

## Global output option

| Option | Description |
|---|---|
| `--json` | Emit machine-readable JSON. In service mode this selects one JSON log object per line. Every one-shot command emits one JSON document; JSON errors are written to standard error. |

Specifying `--json` more than once is a usage error.

## Service

```text
kmonad-device-manager [--json]
```

Service mode is selected when no one-shot command is present. It continuously
discovers `.kbd` files in the configured directory, waits for their input
devices, validates each configuration, and supervises one KMonad process per
available keyboard. It watches configuration and device directories while
retaining polling as a fallback. The process runs until `SIGINT` or `SIGTERM`;
this is the invocation used by the supplied systemd user unit. It requires the
Linux evdev backend and otherwise exits with the structured
`unsupported_platform` error.

### Options

| Option | Description |
|---|---|
| `--json` | Write operational logs as JSON Lines to standard error. Each object contains `time`, `event`, `message`, and event-specific fields. KMonad output is wrapped as `kmonad_stdout` or `kmonad_stderr` with an `output` field so raw child text cannot corrupt the stream. This is equivalent to setting `KMONAD_LOG_FORMAT=json` for the invocation. |

### Examples

```sh
kmonad-device-manager
kmonad-device-manager --json
```

## Doctor

```text
kmonad-device-manager --doctor [--json]
```

Doctor checks Linux evdev platform support, KMonad availability, manager settings, membership in the
`input` and `uinput` groups, the `uinput` kernel module and device access,
configuration-directory security, configured input availability, KMonad
dry-run parsing, and systemd user-service state. A disconnected configured
keyboard is a waiting condition rather than a required setup failure. The exit
status is the number of failed required checks, capped at 255. On an unsupported
platform backend, it emits one `platform_unsupported` diagnostic without
attempting Linux-specific checks.

### Options

| Option | Description |
|---|---|
| `--json` | Return `command`, `config_dir`, `healthy`, `failures`, `waiting`, and `checks`. Each check is a Diagnostic with stable `id`, `severity` (`ok`, `temporary`, `warning`, or `error`), `reason_code`, `summary`, `remediation`, and optional `resource`. JSON never contains ANSI color escapes. |

### Examples

```sh
kmonad-device-manager --doctor
kmonad-device-manager --doctor --json
```

## Status

```text
kmonad-device-manager --status [--json]
```

Status reads the manager's atomically published runtime snapshot and verifies
that its PID and process start time identify the currently running manager. It
reports every known configuration with state, detailed device availability and
reason codes, process health, process identity, retry timing, and failure
context. Availability is `connected`, `disconnected`, `inaccessible`,
`unsupported`, or `conflicting`. Exit status 3 means that the manager is not
running; stale status data is never presented as live state.

### Options

| Option | Description |
|---|---|
| `--json` | Return the complete status document with manager identity, update time, configuration directory, and configuration records. Each record includes `availability`, `availability_reason_code`, and `reason_code`. Errors are JSON objects on standard error. |

### Examples

```sh
kmonad-device-manager --status
kmonad-device-manager --status --json
```

## Ps

```text
kmonad-device-manager ps [--json]
```

`ps` is a process-list-style alias for `--status`. It reads and verifies the
same snapshot and has identical fields, output behavior, and exit statuses.

### Options

| Option | Description |
|---|---|
| `--json` | Emit the same status document as `--status --json`; errors are JSON objects on standard error. |

### Examples

```sh
kmonad-device-manager ps
kmonad-device-manager ps --json
```

## Devices

```text
kmonad-device-manager devices [--json]
```

List known keyboard-capable Linux input interfaces. The command does not
require the manager service to be running. It reports opaque manager device IDs,
display name, vendor, product, serial when available, availability, identity
stability, and a reason code; it never prints platform input paths. Availability
is `connected`, `disconnected`, `inaccessible`, `unsupported`, or `conflicting`.

### Options

| Option | Description |
|---|---|
| `--json` | Return a `devices` array. Each item has an opaque `id`, display metadata, `availability`, `identity_stability`, `configured_by`, `runtime_conflict`, and `reason_code`. Errors are JSON objects on standard error. |

### Examples

```sh
kmonad-device-manager devices
kmonad-device-manager devices --json
```

## Manager

```text
kmonad-device-manager manager get [--json]
```

Read public metadata from the running manager without refreshing device
inventory or altering configuration state. The response identifies the API and
manager versions, KMonad version/compatibility, platform/backend versions, manager instance, current revision and
event cursor, public request/retention limits, capability availability, and
operational health. It never includes filesystem paths, device nodes, process
identifiers, or cgroup details.

### Options

| Option | Description |
|---|---|
| `--json` | Return `api_versions`, `manager_version`, `server_id`, `platform`, `platform_version`, `backend`, `backend_version`, `kmonad`, `state_revision`, `event_cursor`, `limits`, `capabilities`, `limitations`, and `health`. `capabilities` reports per-device mapping and the supported `device_file` input target; `limitations` lists stable feature boundaries. `kmonad` reports bounded startup version detection and current executable availability, without exposing its path. Errors are JSON objects on standard error. |

### Examples

```sh
kmonad-device-manager manager get
kmonad-device-manager manager get --json
```

The command exits 0 after a successful manager response, 1 if the manager is
unavailable or rejects the request, and 2 for invalid arguments.

## Snapshot

```text
kmonad-device-manager snapshot [--json]
```

Read one coherent public-state view from the running manager. The snapshot
contains connected and known-disconnected devices, managed and read-only
external configurations, retained operations, public diagnostics, manager health, a monotonic
state revision, and a resumable event cursor. It does not expose process IDs, filesystem paths, or device
nodes, and API/CLI failure does not affect reconciliation.

### Options

| Option | Description |
|---|---|
| `--json` | Return `state_revision`, `event_cursor`, `devices`, `configurations`, `operations`, `diagnostics`, and `health`. `event_cursor` identifies the manager instance and latest event for safe event resumption. Each Diagnostic has stable ID, severity, reason code, summary, remediation, and optional resource. `health` includes stable reason data, last progress time when available, counters, and metrics/status availability. Errors are JSON objects on standard error. |

### Examples

```sh
kmonad-device-manager snapshot
kmonad-device-manager snapshot --json
```

The command exits 0 after a successful manager response, 1 if the manager is
unavailable or rejects the request, and 2 for invalid arguments.

## Events

```text
kmonad-device-manager events subscribe [--after EVENT_ID] [--server SERVER_ID] [--json]
```

Stream ordered public state-transition events from the running manager. With
`--after`, retained events newer than `EVENT_ID` are replayed before live
events. Pair a resumed cursor with the snapshot `event_cursor.server_id` using
`--server`; a manager restart, expired/future cursor, or slow client produces
`manager.resync_required` and ends the stream. Fetch `snapshot` and subscribe
again. The stream cannot block or control reconciliation.

### Arguments

| Argument | Description |
|---|---|
| `EVENT_ID` | Optional non-negative event sequence from a prior stream; used with `--after`. |
| `SERVER_ID` | Server ID from `snapshot` `event_cursor`; used with `--server` when resuming. |

### Options

| Option | Description |
|---|---|
| `--after EVENT_ID` | Replay retained events with IDs greater than this value before following live events. |
| `--server SERVER_ID` | Require this snapshot `event_cursor.server_id` when resuming with `--after`. |
| `--json` | Write one public Event JSON object per line until the stream ends or a resynchronization event arrives. Errors are JSON objects on standard error. |

### Examples

```sh
kmonad-device-manager events subscribe --json
kmonad-device-manager events subscribe --after 42 --server srv_0123 --json
```

The command exits 0 after a normal stream close or `manager.resync_required`,
1 if the manager is unavailable or rejects the request, and 2 for invalid
arguments.

## Identify

```text
kmonad-device-manager identify { start DEVICE_ID [--timeout SECONDS] | status OPERATION_ID | cancel OPERATION_ID } [--json]
kmonad-device-manager identify start DEVICE_ID [--timeout SECONDS] [--json]
kmonad-device-manager identify status OPERATION_ID [--json]
kmonad-device-manager identify cancel OPERATION_ID [--json]
```

Ask the running manager to wait for a keypress from an opaque `DEVICE_ID`
reported by `devices`. To account for KMonad's input grab, the manager pauses
only the KMonad configuration using that device and restores it after success,
timeout, cancellation, or hotplug. Other configured keyboards continue running.
Only one identification session may run at once.

### Arguments

| Argument | Description |
|---|---|
| `DEVICE_ID` | Opaque device ID from `devices`; required by `start`. |
| `OPERATION_ID` | Opaque operation ID returned by `start`; required by `status` and `cancel`. |

### Options

| Option | Description |
|---|---|
| `--timeout SECONDS` | `start` only. Wait from 1 through 30 seconds; default: 15 seconds. |
| `--json` | Return an `operation` object with ID, state, resource, timestamps, `reason_code`, and `reason`. Errors are JSON objects on standard error. |

### Examples

```sh
kmonad-device-manager identify start dev_0123 --timeout 10
kmonad-device-manager identify status op_0123 --json
kmonad-device-manager identify cancel op_0123
```

`start`, `status`, and `cancel` exit 0 after a successful manager API response,
even if a returned operation has a terminal `failed`, `cancelled`, or `succeeded`
state. They exit 1 when the manager is unavailable or rejects the request, and
2 for invalid command arguments.

## Validate

```text
kmonad-device-manager validate { model MODEL_FILE | file KBD_FILE } [--json]
kmonad-device-manager validate model MODEL_FILE [--json]
kmonad-device-manager validate file KBD_FILE [--json]
```

Ask the running manager to validate one candidate without applying it. `model`
reads a JSON object containing `device_id` and `behavior`; the manager renders
the private Linux input target. `file` reads KMonad candidate text directly.
The manager checks size, device availability and conflicts, validates an
immutable snapshot in its runtime directory with `kmonad --dry-run`, then
removes the snapshot. It never writes the watched configuration directory or
changes a running mapping.

### Arguments

| Argument | Description |
|---|---|
| `MODEL_FILE` | JSON model containing `device_id` and `behavior`; required by `model`. |
| `KBD_FILE` | KMonad candidate text; required by `file`. |

### Options

| Option | Description |
|---|---|
| `--json` | Return a `validation` object with `outcome`, `reason_code`, `reason`, and structured `diagnostics`. Errors are JSON objects on standard error. |

### Examples

```sh
kmonad-device-manager validate model candidate.json --json
kmonad-device-manager validate file candidate.kbd
```

The command exits 0 after a successful manager API response, including a
returned `rejected` or `blocked` validation result. It exits 1 when the manager
is unavailable or rejects the request, and 2 for invalid command arguments.

## Apply

```text
kmonad-device-manager apply MODEL_FILE [--name NAME] [--id CONFIGURATION_ID --revision REVISION] [--json]
```

Read a JSON managed model containing `device_id` and `behavior`, then ask the
running manager to revalidate it, write an immutable revision under its
manager-owned state directory, and activate only that mapping. The command
never writes the external watched configuration directory. A new configuration
requires `--name`; an update requires both `--id` and its current `--revision`
so a concurrent edit cannot be overwritten. Activation is confirmed only after
KMonad starts, joins its cgroup, and passes the manager ownership and health
check. If an update fails activation, the manager stops the unconfirmed
replacement, restores the prior immutable revision, and reports `rolled_back`
only when that known-good mapping passes its health check again. A failed new
configuration create removes its metadata rather than leaving an unconfirmed
mapping.

### Arguments

| Argument | Description |
|---|---|
| `MODEL_FILE` | JSON managed model containing `device_id` and `behavior`. |
| `NAME` | Display name required for a new configuration. |
| `CONFIGURATION_ID` | Opaque managed configuration ID to update. |
| `REVISION` | Current positive revision required for an update. |

### Options

| Option | Description |
|---|---|
| `--name NAME` | Set a new configuration's display name, or rename an existing configuration. |
| `--id CONFIGURATION_ID` | Select the existing managed configuration to update. |
| `--revision REVISION` | Require the current revision for an update; a stale revision is rejected. |
| `--json` | Return an `operation` with state, reason code, reason, resource, `configuration_revision` for the next update, and final validation result. Errors are JSON objects on standard error. |

### Examples

```sh
kmonad-device-manager apply laptop.json --name 'Laptop keyboard' --json
kmonad-device-manager apply laptop.json --id cfg_0123 --revision 1 --json
```

The command exits 0 after a successful manager API response, including a
terminal failed or rejected operation. It exits 1 when the manager is
unavailable or rejects the request, and 2 for invalid command arguments.

## Config

```text
kmonad-device-manager config { list | create MODEL_FILE --name NAME | update CONFIGURATION_ID REVISION MODEL_FILE [--name NAME] | enable CONFIGURATION_ID REVISION | disable CONFIGURATION_ID REVISION | delete CONFIGURATION_ID REVISION | adopt EXTERNAL_CONFIGURATION_ID [--name NAME] } [--json]
```

`list` inventories manager-owned and external configurations without exposing
platform paths. It reports the durable desired revision separately from the
last health-confirmed active revision, the current runtime/retry state, and the
most recent retained operation. External `.kbd` files are read-only unless `adopt` can
losslessly represent one canonical `defcfg` device-file input form. Adoption
never rewrites the source file, requires unchanged source bytes through managed
activation, and hands supervision to the new managed configuration only after
validation and activation confirmation. `create` and `update` use the same transactional validation,
activation confirmation, and rollback behavior as `apply`. `enable` preserves
the configuration's desired state so it starts or recovers automatically when
the selected keyboard reconnects. `disable` stops only that configuration while
retaining its immutable revision. `delete` stops that configuration and removes
its manager-owned revisions. Each mutation requires the current revision
returned as `configuration_revision` by the prior operation, preventing
concurrent overwrites. A manager-owned revision changed outside the manager is
listed as failed and cannot be silently overwritten.

### Arguments

| Argument | Description |
|---|---|
| `MODEL_FILE` | JSON managed model containing `device_id` and `behavior`; required by `create` and `update`. |
| `NAME` | Display name required by `create` and optional for `update`. |
| `CONFIGURATION_ID` | Opaque ID returned by `create`; required by `update`, `enable`, `disable`, and `delete`. |
| `EXTERNAL_CONFIGURATION_ID` | Opaque external ID returned by `list`; required by `adopt`. |
| `REVISION` | Current positive configuration revision required by `update`, `enable`, `disable`, and `delete`. |

### Options

| Option | Description |
|---|---|
| `--name NAME` | Required by `create` and optional on `update` or `adopt`; sets the managed display name. |
| `--json` | `list` returns a `configurations` array with ownership, desired and active revisions, runtime state (including `retry_at` when scheduled), and `last_operation`. Mutations return an `operation` with state, reason code, reason, resource, and `configuration_revision`. Errors are JSON objects on standard error. |

### Examples

```sh
kmonad-device-manager config list --json
kmonad-device-manager config adopt cfg_0123 --name 'Imported keyboard' --json
kmonad-device-manager config create laptop.json --name 'Laptop keyboard' --json
kmonad-device-manager config update cfg_0123 1 laptop.json --json
kmonad-device-manager config enable cfg_0123 2 --json
kmonad-device-manager config disable cfg_0123 3 --json
kmonad-device-manager config delete cfg_0123 4 --json
```

The command exits 0 after a successful manager API response, including a
terminal `rejected`, `failed`, or `rolled_back` operation. It exits 1 when the
manager is unavailable or rejects the request, and 2 for invalid command
arguments.

## Completion

```text
kmonad-device-manager --completion SHELL [--json]
```

Completion prints the definition embedded in the installed executable. This
keeps generated completions aligned with the exact installed version without
requiring access to the source tree.

### Arguments

| Argument | Description |
|---|---|
| `SHELL` | Required target shell. Supported values are `bash`, `zsh`, and `fish`. Any other value is a usage error. |

### Options

| Option | Description |
|---|---|
| `--json` | Return `shell` and `completion`. `completion` contains the complete definition as a JSON string rather than printing executable shell syntax directly. |

### Examples

```sh
source <(kmonad-device-manager --completion bash)
kmonad-device-manager --completion zsh > ~/.zfunc/_kmonad-device-manager
kmonad-device-manager --completion fish --json
```

## Version

```text
kmonad-device-manager --version [--json]
```

Version prints the value injected at build time. Release binaries report their
release version; development builds without an injected version report `dev`.

### Options

| Option | Description |
|---|---|
| `--json` | Return `program` and `version` fields. |

### Examples

```sh
kmonad-device-manager --version
kmonad-device-manager --version --json
```

## Help

```text
kmonad-device-manager {-h|--help} [--json]
```

Help prints the complete in-program command index. It includes every public
invocation, argument, option, output contract, example, and exit status. It is
designed to carry the same command information as this Wiki page and the man
page.

### Options

| Option | Description |
|---|---|
| `-h`, `--help` | Select the help command. The two spellings are equivalent. |
| `--json` | Return structured help metadata with `program`, `summary`, `usage`, `commands`, `exit_status`, and `further_help`. Each command contains its arguments, options, JSON contract, and examples. |

### Examples

```sh
kmonad-device-manager --help
kmonad-device-manager --help --json
```

## Exit statuses

| Status | Meaning |
|---|---|
| `0` | The command succeeded; for doctor, no required check failed. |
| `1` | A runtime, dependency, setup, lock, or status-reading operation failed. |
| `2` | Command-line arguments or manager settings are invalid, or the requested operation requires the unavailable Linux evdev backend. |
| `3` | A status or `ps` query found that the manager is not running. |
| `127` | Service mode could not find the configured KMonad executable. |

For configuration, environment variables, status-state meanings, security,
installation, and service details, see `man kmonad-device-manager`.
