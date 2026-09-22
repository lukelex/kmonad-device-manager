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
| [devices](#devices) | `kmonad-device-manager devices [--json]` | List connected keyboard-capable input interfaces. |
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
reports every known configuration with state, device connection, process
health, process identity, retry timing, and failure context. Exit status 3
means that the manager is not running; stale status data is never presented as
live state.

### Options

| Option | Description |
|---|---|
| `--json` | Return the complete status document with manager identity, update time, configuration directory, and configuration records. Errors are JSON objects on standard error. |
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

List currently connected keyboard-capable Linux input interfaces. The command
does not require the manager service to be running. It reports opaque manager
device IDs, display name, vendor, product, serial when available, availability,
and identity stability; it never prints platform input paths.

### Options

| Option | Description |
|---|---|
| `--json` | Return a `devices` array. Each item has an opaque `id`, display metadata, `availability`, `identity_stability`, and `reason_code`. Errors are JSON objects on standard error. |

### Examples

```sh
kmonad-device-manager devices
kmonad-device-manager devices --json
```

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
