# Installation

KMonad Device Manager is a Linux **systemd user service**. Every installation
method installs the manager on the host; Docker is only an optional way to
produce the binary. You must also install KMonad separately and have one or
more `.kbd` files containing an `input (device-file "...")` declaration.

## Choose an installation method

| Method | Best for | Go required? |
| --- | --- | --- |
| Arch Linux AUR | Arch users who want package management | No |
| Release archive | Most users on other distributions | No |
| Source installer | Users who want to build locally | Yes |
| Docker-assisted source build | Reproducible builds without local Go | Docker only |

All methods install the same static manager executable. The installer also
configures the `input` and `uinput` groups, the udev rule, the `uinput` kernel
module, the systemd user service, shell completions, and the man page.

## Prerequisites

Before installing, install [KMonad](https://github.com/kmonad/kmonad) with your
distribution's package manager or its upstream instructions. The installer
checks that `kmonad` is available. On Arch Linux, the source and archive
installers can install it automatically with `--install-kmonad`.

The host also needs a user systemd session, `sudo`, and the usual Linux tools
provided by `util-linux`, `shadow`, and `systemd`. The installer must be run as
your regular user, not as root.

## Arch Linux: AUR

Install the package with an AUR helper:

```sh
yay -S kmonad-device-manager
```

Then add your user to the required groups and enable the service:

```sh
sudo usermod -aG input,uinput "$USER"
sudo modprobe uinput
systemctl --user enable --now kmonad-device-manager.service
```

Log out and back in after the group change. The package installs the udev rule,
persistent module-loading configuration, service, completions, and man page.

## Linux: release archive

1. Download the archive for your architecture from the project's [GitHub
   Releases](https://github.com/lukelex/kmonad-device-manager/releases). Use
   `amd64` for x86-64 systems and `arm64` for 64-bit ARM systems.
2. Verify the archive. Release files include SHA-256 checksums, an SPDX SBOM,
   and GitHub artifact attestations. From the directory containing the release
   files, run:

   ```sh
   sed 's#dist/##' kmonad-device-manager-VERSION-linux-ARCH.sha256 \
     | sha256sum -c -
   ```

   Replace `VERSION` and `ARCH` with the names in the downloaded file. If you
   downloaded the combined `SHA256SUMS` file, verify all downloaded archives
   with `sed 's#dist/##' SHA256SUMS | sha256sum -c -`.
3. Extract and run the installer:

   ```sh
   mkdir kmonad-device-manager-release
   tar -xzf kmonad-device-manager-VERSION-linux-ARCH.tar.gz \
     -C kmonad-device-manager-release
   cd kmonad-device-manager-release
   ./install.sh --binary ./kmonad-device-manager
   ```

   If KMonad is already installed, this is all that is required. On Arch Linux,
   add `--install-kmonad` if it is not installed:

   ```sh
   ./install.sh --binary ./kmonad-device-manager --install-kmonad
   ```

   To verify release provenance, use `gh attestation verify <archive>
   --owner lukelex`.

Choose a different configuration directory with, for example,
`--config-dir "$HOME/dotfiles/kmonad"`.

Re-running the installer updates only the managed `KMONAD_CONFIG_DIR` setting
and preserves other settings. If manually launched KMonad processes already
exist, the installer enables the manager but does not start it to prevent
duplicate remappers. Stop those processes, then run
`systemctl --user start kmonad-device-manager.service`.

The default configuration directory is `~/.config/kmonad`; existing files are
never changed.

## Linux: build from source

Install Go 1.27 or newer, clone the repository, and run the installer:

```sh
git clone https://github.com/lukelex/kmonad-device-manager.git
cd kmonad-device-manager
./install.sh
```

The installer builds with `CGO_ENABLED=0`, installs the resulting binary as
`~/.local/bin/kmonad-device-manager`, and configures the host integration. To
build manually instead:

```sh
CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' \
  -o kmonad-device-manager ./cmd/kmonad-device-manager
./install.sh --binary ./kmonad-device-manager
```

Do not run the manager inside the source tree as a substitute for installation:
the service needs host access to input devices, `uinput`, and the user systemd
session.

## Docker-assisted source build

The repository includes a Dockerfile that produces the same static release
binary used by CI. Docker builds the artifact, but the resulting manager must
be installed and run on the host.

From a repository checkout, build the release stage and copy out the binary:

```sh
docker build --target release -t kmonad-device-manager-build .
container_id="$(docker create kmonad-device-manager-build)"
docker cp "$container_id:/kmonad-device-manager" ./kmonad-device-manager
docker rm "$container_id"
chmod +x ./kmonad-device-manager
```

Install the extracted binary on the host:

```sh
./install.sh --binary ./kmonad-device-manager
```

For a versioned build, pass the version used by the executable:

```sh
docker build --target release --build-arg VERSION=1.2.3 \
  -t kmonad-device-manager-build .
```

Docker does **not** provide the host's input devices, `uinput`, udev, groups,
or user systemd service. Do not run the manager as a Docker container.

## Configuration and first start

Place `.kbd` files in `~/.config/kmonad`, or pass `--config-dir PATH` to the
installer. On a new installation, log out and back in so the new group
membership reaches the user session. Then check the setup:

```sh
kmonad-device-manager --doctor
kmonad-device-manager --status
```

For keyboard configuration files and runtime settings, see the [keyboard
configuration guide](https://github.com/lukelex/kmonad-device-manager/wiki/Keyboard-Configuration).

The full local manual is available after installation with:

```sh
man kmonad-device-manager
```

## Uninstall

From a repository checkout, run:

```sh
./uninstall.sh
```

This removes only the user service, manager binary, completion files, and
manager environment file. It leaves KMonad, keyboard configs, group membership,
and system udev settings intact.

## AppImage

An AppImage is not provided. It is a poor fit for this project because it only
bundles the manager executable; it cannot replace the separately installed
KMonad dependency or configure host input permissions, udev, `uinput`, and the
systemd user service. Release archives and the installer provide the same
single-binary convenience while handling those host requirements correctly.
