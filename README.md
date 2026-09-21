# KMonad Device Manager

<p align="center">
  <img src="assets/logo.svg" alt="KMonad Device Manager — a keyboard connected to three device nodes" width="800">
</p>

Run one [KMonad](https://github.com/kmonad/kmonad) process for every configured keyboard that is currently connected.

KMonad Device Manager scans a directory of `.kbd` files, reads each configuration's `device-file`, and starts KMonad only when that device is available. It stops the matching process when the device disappears, restarts a failed process while its device remains available, and runs all matching configurations concurrently.

This is a companion service, not a KMonad replacement. All remapping behavior and configuration syntax belong to [KMonad](https://github.com/kmonad/kmonad).

KMonad is an excellent keyboard remapping engine, but managing one process per
keyboard by hand becomes tedious when devices are plugged in, disconnected, or
reconfigured. This project automates that lifecycle so KMonad can focus on the
remapping while the manager handles discovery, validation, starting, stopping,
and reloading.

Many thanks to [@kmonad](https://github.com/kmonad) and the KMonad project, originally created by [David Janssen](https://github.com/david-janssen), for the keyboard remapping engine this project manages.

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

## Requirements

- Linux with systemd user services
- Go 1.27 or newer when installing from source
- [KMonad](https://github.com/kmonad/kmonad) installed and available as `kmonad`
- One or more KMonad `.kbd` files containing `input (device-file "...")`

The source installer uses standard systemd, udev, shadow-utils, `sudo`, and Go
tooling, so it is not tied to a specific Linux distribution. Install KMonad
using your distribution's package manager or the [upstream installation
instructions](https://github.com/kmonad/kmonad/blob/master/doc/installation.md).
The installer exits with a clear error if `kmonad` is unavailable.

## Install

### Arch Linux

Install the AUR package:

```sh
yay -S kmonad-device-manager
```

The package builds and installs the same statically linked Go executable as the
source installer.

Release archives include SHA-256 checksums, an SPDX SBOM, and GitHub artifact
attestations. Verify an archive with `sha256sum -c SHA256SUMS`; verify its
provenance with `gh attestation verify <archive> --owner lukelex`.

Then add your user to the required groups, log out and back in, and enable the
user service:

```sh
sudo usermod -aG input,uinput "$USER"
systemctl --user enable --now kmonad-device-manager.service
```

The package creates the `uinput` system group and installs the udev rule and
persistent module-loading configuration. Load the module immediately with
`sudo modprobe uinput`, or reboot.

### Other distributions

Clone the repository and run:

```sh
./install.sh
```

The installer builds a statically linked Go executable and installs it as
`~/.local/bin/kmonad-device-manager`. It works for both new and existing
systems. It:

- installs the `uinput` udev rule and persistent module loading;
- adds the current user to the `input` and `uinput` groups;
- installs and enables `kmonad-device-manager.service`;
- starts it immediately when the current session already has the required groups.
- installs the man page as `~/.local/share/man/man1/kmonad-device-manager.1`.

The installer does not require the AUR. To install a prebuilt binary downloaded
from a GitHub release, clone or download the repository files and run:

```sh
./install.sh --binary ./kmonad-device-manager
```

This skips the Go build while installing the same user service, permissions,
completions, and man page. Release archives are self-contained installers and
include `install.sh`, the service assets, and the man page under
`share/man/man1/`.

Re-running the installer updates only the managed `KMONAD_CONFIG_DIR` setting in
the environment file and preserves other settings.

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

It checks KMonad, group membership, the `uinput` kernel module and device
permissions, configuration/device readiness, KMonad config parsing,
configuration ownership and permissions, and user-service state. Green checks
are ready, yellow waiting checks are
configured keyboards that are currently disconnected, and red checks need
attention. It exits nonzero when any required check fails. Set
`KMONAD_DOCTOR_COLOR=never` to disable ANSI colors.

View the current manager state and managed child PIDs with:

```sh
kmonad-device-manager --status
kmonad-device-manager --status=json
kmonad-device-manager ps
```

After installation, read the full local manual with:

```sh
man kmonad-device-manager
```

## Shell Completion

Bash, Zsh, and Fish completion files are embedded in the executable and also
installed by `install.sh`. For a manual Bash or Zsh setup, print and source the
appropriate definition:

```sh
source <(kmonad-device-manager --completion bash)
```

Replace `bash` with `zsh` as needed. Zsh needs its user completion directory in `fpath` before `compinit` runs:

```zsh
fpath=("${XDG_DATA_HOME:-$HOME/.local/share}/zsh/site-functions" $fpath)
autoload -Uz compinit && compinit
```

For Fish, use:

```fish
kmonad-device-manager --completion fish | source
```

## Configure your keyboards

### Basic configuration

Put one or more `.kbd` files in the configured directory. Each file must declare a distinct input device:

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

Editing or replacing an existing `.kbd` file is also detected automatically and
reloads its KMonad process without restarting the manager service. The manager
creates a validated snapshot, runs `kmonad --dry-run`, and only then replaces
the running process. If validation fails, the previous known-good process stays
running and the update is retried later. This watches `.kbd` files in the
configured directory; KMonad include/import files are not watched separately.

Before every launch, the manager validates the KMonad configuration with `kmonad --dry-run`. It only accepts readable character devices, detects a changed device behind a stable symlink, prevents duplicate configurations from reading the same input device, and exponentially backs off failed launches. When the primary configuration for a duplicate device is removed, the next matching configuration takes over.

### Advanced settings

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

Set `KMONAD_WATCHDOG_TIMEOUT` to control how long an unhealthy KMonad process
may remain before it is stopped and retried; the default is 60 seconds. Use
`kmonad-device-manager ps` (or `--status`) to list every known configuration,
device connection, health, process ID, retry state, and current reason.

For optional per-process cgroup isolation, set `KMONAD_CGROUP_ROOT` to a
delegated cgroup v2 directory. The manager creates one child cgroup per
configuration and moves each KMonad process into it. Set
`KMONAD_PROCESS_MEMORY_MAX` and/or `KMONAD_PROCESS_CPU_MAX` to write
`memory.max` and `cpu.max` limits. Isolation is disabled unless
`KMONAD_CGROUP_ROOT` is explicitly configured; if enabled and the cgroup
cannot be created, the process is not started.

### Reliability details

Only one manager instance is allowed per user. The Go process supervisor uses
process groups and Linux parent-death handling for clean shutdown, and refuses
to start while an existing KMonad process is running. Configuration files are
read transactionally so an editor cannot expose a partially written config.
Fatal settings errors, missing KMonad, and lock contention are not restarted
indefinitely by systemd.

View manager and child-process logs with:

```sh
journalctl --user -u kmonad-device-manager.service -f
```

Set `KMONAD_LOG_FORMAT=json` for machine-readable manager log entries.

## Tests

Run the Go unit tests and Go-backed integration test suite with:

```sh
go test ./...
./tests/run.sh
./tests/install.sh
```

CI enforces a minimum Go statement coverage threshold and publishes the full
coverage report as a workflow artifact.

The integration suite uses a fake KMonad process and temporary device files to
cover configuration parsing, concurrent devices, duplicate-device failover,
symlink target replacement, disconnect cleanup, configuration removal, crash
recovery, missing-KMonad errors, locking, completions, and service syntax.

To exercise cgroup behavior against a real delegated cgroup v2 subtree, set
`KMONAD_TEST_CGROUP_ROOT` and run the Go tests. The test is skipped when that
environment variable is not configured.

To test a real user-systemd manager lifecycle, run `KMONAD_TEST_SYSTEMD=1
./tests/systemd-user.sh` from a logged-in Linux user session. It is opt-in so
normal development and container tests do not require a user systemd bus.

Build the executable manually with:

```sh
CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o kmonad-device-manager ./cmd/kmonad-device-manager
```

### Docker development environment

Build the development image and open an interactive shell:

```sh
docker compose run --rm dev bash
```

Run the complete containerized validation suite:

```sh
docker compose run --rm test
```

The Compose environment is intended for building and testing the manager. It
does not expose host keyboard devices or run the systemd user service.

## Uninstall

```sh
./uninstall.sh
```

This removes only the user service, manager binary, completion files, and
manager environment file. It leaves KMonad, keyboard configs, group
membership, and system udev settings intact.

## License

MIT. KMonad itself is a separate MIT-licensed project by David Janssen and contributors.
