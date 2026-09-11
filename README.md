# KMonad Device Manager

Run one [KMonad](https://github.com/kmonad/kmonad) process for every configured keyboard that is currently connected.

KMonad Device Manager scans a directory of `.kbd` files, reads each configuration's `device-file`, and starts KMonad only when that device is available. It stops the matching process when the device disappears, restarts a failed process while its device remains available, and runs all matching configurations concurrently.

This is a companion service, not a KMonad replacement. All remapping behavior and configuration syntax belong to [KMonad](https://github.com/kmonad/kmonad).

Many thanks to [@kmonad](https://github.com/kmonad) and the KMonad project, originally created by [David Janssen](https://github.com/david-janssen), for the keyboard remapping engine this project manages.

## Requirements

- Linux with systemd user services
- [KMonad](https://github.com/kmonad/kmonad) installed and available as `kmonad`
- One or more KMonad `.kbd` files containing `input (device-file "...")`

The installer uses standard systemd, udev, shadow-utils, and `sudo` tooling, so it is not tied to a specific Linux distribution. Install KMonad using your distribution's package manager or the [upstream installation instructions](https://github.com/kmonad/kmonad/blob/master/doc/installation.md). The installer exits with a clear error if `kmonad` is unavailable.

## Install

Clone the repository and run:

```sh
./install.sh
```

The installer works for both new and existing systems. It:

- installs the `uinput` udev rule and persistent module loading;
- adds the current user to the `input` and `uinput` groups;
- installs and enables `kmonad-device-manager.service`;
- starts it immediately when the current session already has the required groups.

On a first install, log out and back in before the service starts. Group membership cannot be applied to an existing session.

If manually launched KMonad processes already exist, the installer enables the manager but does not start it, preventing duplicate remappers. Stop the old processes, then run `systemctl --user start kmonad-device-manager.service`.

On Arch Linux only, install KMonad automatically when needed:

```sh
./install.sh --install-kmonad
```

Use an existing configuration directory instead of the default:

```sh
./install.sh --config-dir "$HOME/dotfiles/linux/config/kmonad"
```

The default configuration directory is `~/.config/kmonad`. Existing configuration files are never changed.

## Doctor

Run a color-coded dependency report with:

```sh
kmonad-device-manager --doctor
```

It checks KMonad and required runtime commands, group membership, the `uinput` kernel module and device permissions, configuration/device readiness, KMonad config parsing, and user-service state. Green checks are ready, yellow waiting checks are configured keyboards that are currently disconnected, and red checks need attention. It exits nonzero when any required check fails. Set `KMONAD_DOCTOR_COLOR=never` to disable ANSI colors.

## Configuration

Put one or more `.kbd` files in the configured directory. Each file must declare a distinct input device:

```lisp
(defcfg
  input  (device-file "/dev/input/by-id/usb-example-event-kbd")
  output (uinput-sink "Example keyboard")
)
```

The manager checks configurations every two seconds. Connecting a keyboard starts its matching configuration; disconnecting it stops the corresponding KMonad process. Adding or removing `.kbd` files is detected automatically.

Before every launch, the manager validates the KMonad configuration with `kmonad --dry-run`. It only accepts readable character devices, detects a changed device behind a stable symlink, prevents duplicate configurations from reading the same input device, and exponentially backs off failed launches. When the primary configuration for a duplicate device is removed, the next matching configuration takes over.

View manager and child-process logs with:

```sh
journalctl --user -u kmonad-device-manager.service -f
```

## Tests

Run the dependency-free Bash test suite with:

```sh
./tests/run.sh
```

It uses a fake KMonad process and temporary device files to cover configuration parsing, concurrent devices, duplicate-device failover, symlink target replacement, disconnect cleanup, configuration removal, invalid-config and crash-loop backoff, missing-KMonad errors, and service syntax.

## Uninstall

```sh
./uninstall.sh
```

This removes only the user service, manager binary, and manager environment file. It leaves KMonad, keyboard configs, group membership, and system udev settings intact.

## License

MIT. KMonad itself is a separate MIT-licensed project by David Janssen and contributors.
