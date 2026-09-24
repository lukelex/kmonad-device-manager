# KMonad Device Manager

<p align="center">
  <img src="assets/logo.svg" alt="KMonad Device Manager — a keyboard connected to three device nodes" width="800">
</p>

KMonad is an excellent keyboard remapping engine, but managing one process per
keyboard by hand becomes tedious when devices are plugged in, disconnected, or
reconfigured. This project automates that lifecycle so KMonad can focus on the
remapping while the manager handles discovery, validation, starting, stopping,
and reloading.

Many thanks to [@kmonad](https://github.com/kmonad) and the KMonad project, originally created by [David Janssen](https://github.com/david-janssen), for the keyboard remapping engine this project manages.

Run one [KMonad](https://github.com/kmonad/kmonad) process for every configured keyboard that is currently connected.

KMonad Device Manager scans a directory of `.kbd` files, reads each configuration's `device-file`, and starts KMonad only when that device is available. It stops the matching process when the device disappears, restarts a failed process while its device remains available, and runs all matching configurations concurrently.

This is a companion service, not a KMonad replacement. All remapping behavior and configuration syntax belong to [KMonad](https://github.com/kmonad/kmonad).

## How it works

The manager is a systemd user service that supervises one KMonad process for
each `.kbd` file in its configuration directory. For each configuration, it:

1. Reads the `input (device-file "...")` target and waits for that keyboard to
   be available.
2. Watches the configuration and device directories, with a two-second polling
   fallback.
3. Validates new or changed configurations with `kmonad --dry-run`.
4. Starts KMonad from the exact validated configuration snapshot.
5. Stops, reloads, or retries the affected process as the keyboard, file, or
   process state changes.

If a configuration edit is invalid, the existing known-good KMonad process
continues running while the manager retries the update. Adding, removing, or
editing `.kbd` files does not require restarting the manager service.

## Documentation

- [Installation](https://github.com/lukelex/kmonad-device-manager/wiki/Installation)
- [Keyboard configuration](https://github.com/lukelex/kmonad-device-manager/wiki/Keyboard-Configuration)
- [Command reference](https://github.com/lukelex/kmonad-device-manager/wiki/Command-Reference)
- [Troubleshooting](https://github.com/lukelex/kmonad-device-manager/wiki/Troubleshooting)
- [Development](https://github.com/lukelex/kmonad-device-manager/wiki/Development)
