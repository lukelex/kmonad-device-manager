# Troubleshooting

## Check readiness

Run the diagnostic report after installation:

```sh
kmonad-device-manager --doctor
kmonad-device-manager --doctor --json
```

Doctor checks KMonad, group membership, the `uinput` kernel module and device
permissions, configuration and device readiness, KMonad config parsing,
configuration ownership and permissions, and user-service state. Green checks
are ready, yellow checks are configured keyboards that are currently
disconnected, and red checks need attention. It exits nonzero when a required
check fails. Set `KMONAD_DOCTOR_COLOR=never` to disable ANSI colors.

View the current manager state and managed child processes with:

```sh
kmonad-device-manager --status
kmonad-device-manager --status --json
kmonad-device-manager ps
```

For complete command syntax and JSON output contracts, see the [command
reference](https://github.com/lukelex/kmonad-device-manager/wiki/Command-Reference).

## View service logs

```sh
journalctl --user -u kmonad-device-manager.service -f
```

Set `KMONAD_LOG_FORMAT=json` for machine-readable manager log entries.

## Common lifecycle behavior

Only one manager instance is allowed per user. The process supervisor uses
process groups and Linux parent-death handling for clean shutdown, and refuses
to start while an existing KMonad process is running. Configuration files are
read transactionally so an editor cannot expose a partially written config.
Fatal settings errors, missing KMonad, and lock contention are not restarted
indefinitely by systemd.
