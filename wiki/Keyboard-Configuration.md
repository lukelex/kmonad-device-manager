# Keyboard configuration

## Requirements

Each `.kbd` file must declare a distinct input device using KMonad's
`device-file` input target. Place files in the configured directory, which is
`~/.config/kmonad` by default.

```lisp
(defcfg
  input  (device-file "/dev/input/by-id/usb-example-event-kbd")
  output (uinput-sink "Example keyboard")
)
```

The manager watches configuration and device directories for immediate changes,
with a two-second polling loop retained as a fallback. Connecting a keyboard
starts its matching configuration; disconnecting it stops the corresponding
KMonad process. Adding or removing `.kbd` files is detected automatically.

## Editing configurations

Editing or replacing an existing `.kbd` file is detected automatically and
reloads its KMonad process without restarting the manager service. The manager
creates a validated snapshot, runs `kmonad --dry-run`, and only then replaces
the running process. If validation fails, the previous known-good process stays
running and the update is retried later.

Only `.kbd` files in the configured directory are watched. KMonad
include/import files are not watched separately.

Before every launch, the manager validates the KMonad configuration, accepts
only readable character devices, detects a changed device behind a stable
symlink, prevents duplicate configurations from reading the same input device,
and exponentially backs off failed launches. When the primary configuration
for a duplicate device is removed, the next matching configuration takes over.

## Advanced settings

Set `KMONAD_MAX_CONFIGS` to limit how many configuration files the manager will
consider in one run; the default is 128. The provided systemd services allow
512 tasks for the manager and its children. If you raise the configuration
limit substantially, add a matching `TasksMax` override to the user service.

Set `KMONAD_MAX_CONFIG_BYTES` to reject oversized `.kbd` files; the default is
1 MiB.

Set `KMONAD_DRY_RUN_TIMEOUT` to bound configuration validation; the default is
30 seconds. Configurations move through explicit discovered, validating,
waiting, running, failed, duplicate, and stopped states.

Set `KMONAD_METRICS_ADDR` (for example, `127.0.0.1:9090`) to expose a small
Prometheus-compatible `/metrics` endpoint. Metrics are disabled by default.
Bind it to a loopback address unless a network-accessible metrics endpoint is
intentional; the endpoint has no authentication. Set
`KMONAD_METRICS_ALLOW_REMOTE=1` to explicitly permit a non-loopback bind.

The endpoint exports reconciliation, process, and status counters plus
`kmonad_manager_public_state_revision` and
`kmonad_manager_public_events_total{event_type=...}`. With
`KMONAD_LOG_FORMAT=json`, the same publisher writes sanitized
`manager_transition` JSON Lines containing the event and state revisions,
event type, opaque resource reference, and reason code.

Set `KMONAD_WATCHDOG_TIMEOUT` to control how long an unhealthy KMonad process
may remain before it is stopped and retried; the default is 60 seconds. Use
`kmonad-device-manager ps` or `--status` to list every known configuration,
device connection, health, process ID, retry state, and current reason.

For optional per-process cgroup isolation, set `KMONAD_CGROUP_ROOT` to a
delegated cgroup v2 directory. The manager creates one child cgroup per
configuration and moves each KMonad process into it. Set
`KMONAD_PROCESS_MEMORY_MAX` and/or `KMONAD_PROCESS_CPU_MAX` to write
`memory.max` and `cpu.max` limits. Isolation is disabled unless
`KMONAD_CGROUP_ROOT` is explicitly configured; if enabled and the cgroup
cannot be created, the process is not started.
