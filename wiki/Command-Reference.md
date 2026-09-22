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
| [identify](#identify) | `kmonad-device-manager identify {start DEVICE_ID [--timeout SECONDS]\|status OPERATION_ID\|cancel OPERATION_ID} [--json]` | Run, inspect, or cancel a keypress identification session. |
| [validate](#validate) | `kmonad-device-manager validate {model MODEL_FILE\|file KBD_FILE} [--json]` | Preview one candidate without applying it. |
| [completion](#completion) | `kmonad-device-manager --completion SHELL [--json]` | Print an embedded shell-completion definition. |
| [version](#version) | `kmonad-device-manager --version [--json]` | Show build version metadata. |
| [help](#help) | `kmonad-device-manager {-h\|--help} [--json]` | Show the complete in-program command reference. |

## Global output option

| Option | Description |
|---|---|
| `--json` | Emit machine-readable JSON. In service mode this selects one JSON log object per line. Every one-shot command emits one JSON document; JSON errors are written to standard error. |

Specifying `--json` more than once is a usage error. `--status=json` remains a
compatibility spelling for `--status --json`.

## Service

```text
kmonad-device-manager [--json]
```

Service mode is selected when no one-shot command is present. It continuously
discovers `.kbd` files in the configured directory, waits for their input
devices, validates each configuration, and supervises one KMonad process per
available keyboard. It watches configuration and device directories while
retaining polling as a fallback. The process runs until `SIGINT` or `SIGTERM`;
this is the invocation used by the supplied systemd user unit.

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

Doctor checks KMonad availability, manager settings, membership in the
`input` and `uinput` groups, the `uinput` kernel module and device access,
configuration-directory security, configured input availability, KMonad
dry-run parsing, and systemd user-service state. A disconnected configured
keyboard is a waiting condition rather than a required setup failure. The exit
status is the number of failed required checks, capped at 255.

### Options

| Option | Description |
|---|---|
| `--json` | Return `command`, `config_dir`, `healthy`, `failures`, `waiting`, and `checks`. Each check has `status` (`ok`, `waiting`, or `error`) and `message`. JSON never contains ANSI color escapes. |

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
| `--status=json` | Compatibility spelling for `--status --json`. Do not combine it with another `--json`. |

### Examples

```sh
kmonad-device-manager --status
kmonad-device-manager --status --json
kmonad-device-manager --status=json
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
| `2` | Command-line arguments or manager settings are invalid. |
| `3` | A status or `ps` query found that the manager is not running. |
| `127` | Service mode could not find the configured KMonad executable. |

For configuration, environment variables, status-state meanings, security,
installation, and service details, see `man kmonad-device-manager`.
