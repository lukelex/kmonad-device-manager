#!/usr/bin/env bash

set -Eeuo pipefail

repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
config_dir="${XDG_CONFIG_HOME:-$HOME/.config}/kmonad"
force=0
install_kmonad=0
binary_path=''

usage() {
  cat <<'EOF'
Usage: ./install.sh [--config-dir PATH] [--binary PATH] [--install-kmonad] [--force]

  --config-dir PATH  Directory containing KMonad .kbd files.
  --install-kmonad   Install KMonad with pacman when it is unavailable.
  --binary PATH      Install an existing prebuilt manager binary instead of building with Go.
  --force            Replace existing manager binary or symlinks.
EOF
}

die() {
  printf 'kmonad-device-manager: %s\n' "$1" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --config-dir)
      [ "$#" -ge 2 ] || { usage >&2; exit 2; }
      config_dir="$2"
      shift 2
      ;;
    --install-kmonad)
      install_kmonad=1
      shift
      ;;
    --binary)
      [ "$#" -ge 2 ] || { usage >&2; exit 2; }
      binary_path="$2"
      shift 2
      ;;
    --force)
      force=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *) usage >&2; exit 2 ;;
  esac
done

[ "${EUID}" -ne 0 ] || die 'run this installer as your regular user, not root'

for command in sudo getent groupadd usermod install modprobe sed udevadm systemctl; do
  require_command "$command"
done

if [ -z "$binary_path" ]; then
  require_command go
fi

if ! command -v kmonad >/dev/null 2>&1; then
  if [ "$install_kmonad" -eq 1 ] && command -v pacman >/dev/null 2>&1; then
    sudo pacman -S --needed kmonad
  else
    die 'KMonad is required. Install it from https://github.com/kmonad/kmonad, or use --install-kmonad on Arch Linux.'
  fi
fi

link_file() {
  local source="$1"
  local target="$2"

  mkdir -p "$(dirname "$target")"
  if [ -L "$target" ] && [ "$(readlink -f "$target")" = "$source" ]; then
    return
  fi
  if [ -e "$target" ] || [ -L "$target" ]; then
    [ "$force" -eq 1 ] || {
      printf 'Refusing to replace %s; rerun with --force.\n' "$target" >&2
      exit 1
    }
    rm -f "$target"
  fi
  ln -s "$source" "$target"
}

install_manager() {
  local target="$HOME/.local/bin/kmonad-device-manager"
  local existing_target existing_version build_output build_version

  mkdir -p "$(dirname "$target")"
  if [ -e "$target" ] || [ -L "$target" ]; then
    existing_target=''
    if [ -L "$target" ]; then
      existing_target="$(readlink -f "$target" 2>/dev/null || true)"
    fi
    existing_version=''
    if [ -x "$target" ] && [ ! -L "$target" ]; then
      existing_version="$("$target" --version 2>/dev/null || true)"
    fi
    case "$existing_version" in
      'kmonad-device-manager '*) existing_target="$repo_dir/bin/kmonad-device-manager" ;;
    esac
    if [ "$existing_target" != "$repo_dir/bin/kmonad-device-manager" ] && [ "$force" -ne 1 ]; then
      printf 'Refusing to replace %s; rerun with --force.\n' "$target" >&2
      exit 1
    fi
  fi

  if [ -n "$binary_path" ]; then
    [ -x "$binary_path" ] || die "prebuilt binary is not executable: $binary_path"
    install -m755 "$binary_path" "$target"
  else
    build_output="$(mktemp)"
    build_version="${KMONAD_DEVICE_MANAGER_VERSION:-}"
    if [ -z "$build_version" ] && command -v git >/dev/null 2>&1; then
      build_version="$(git -C "$repo_dir" describe --tags --always --dirty 2>/dev/null || true)"
      build_version="${build_version#v}"
    fi
    if [ -n "$build_version" ]; then
      (cd "$repo_dir" && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=$build_version" -o "$build_output" ./cmd/kmonad-device-manager)
    else
      (cd "$repo_dir" && CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o "$build_output" ./cmd/kmonad-device-manager)
    fi
    install -m755 "$build_output" "$target"
    rm -f "$build_output"
  fi
}

install_manager

if ! getent group input >/dev/null; then
  sudo groupadd --system input
fi
if ! getent group uinput >/dev/null; then
  sudo groupadd --system uinput
fi

sudo usermod -aG input,uinput "$USER"
sudo install -Dm644 "$repo_dir/system/70-kmonad-uinput.rules" /etc/udev/rules.d/70-kmonad-uinput.rules
sudo install -Dm644 "$repo_dir/system/uinput.conf" /etc/modules-load.d/uinput.conf
sudo modprobe uinput
sudo udevadm control --reload-rules
sudo udevadm trigger --action=add --subsystem-match=misc --sysname-match=uinput
sudo udevadm settle

link_file "$repo_dir/systemd/kmonad-device-manager.service" "$HOME/.config/systemd/user/kmonad-device-manager.service"
link_file "$repo_dir/completions/kmonad-device-manager.bash" \
  "${XDG_DATA_HOME:-$HOME/.local/share}/bash-completion/completions/kmonad-device-manager"
link_file "$repo_dir/completions/_kmonad-device-manager" \
  "${XDG_DATA_HOME:-$HOME/.local/share}/zsh/site-functions/_kmonad-device-manager"
link_file "$repo_dir/completions/kmonad-device-manager.fish" \
  "${XDG_CONFIG_HOME:-$HOME/.config}/fish/completions/kmonad-device-manager.fish"
install -Dm644 "$repo_dir/docs/kmonad-device-manager.1" \
  "${XDG_DATA_HOME:-$HOME/.local/share}/man/man1/kmonad-device-manager.1"

mkdir -p "$HOME/.config/kmonad-device-manager"
environment_file="$HOME/.config/kmonad-device-manager/env"
if [ -r "$environment_file" ]; then
  preserved_environment="$(sed '/^[[:space:]]*KMONAD_CONFIG_DIR[[:space:]]*=.*$/d' "$environment_file" || true)"
  printf '%s\n%s\n' "$preserved_environment" "KMONAD_CONFIG_DIR=$config_dir" > "$environment_file"
else
  printf 'KMONAD_CONFIG_DIR=%s\n' "$config_dir" > "$environment_file"
fi

systemctl --user daemon-reload
systemctl --user enable kmonad-device-manager.service

if id -nG "$USER" | tr ' ' '\n' | grep -qx input \
  && id -nG "$USER" | tr ' ' '\n' | grep -qx uinput; then
  if command -v pgrep >/dev/null 2>&1 && pgrep -x kmonad >/dev/null; then
    printf '%s\n' 'Existing KMonad processes detected. The manager is enabled but was not started to avoid duplicates.'
  else
    systemctl --user start kmonad-device-manager.service
  fi
else
  printf '%s\n' 'KMonad permissions are installed. Log out and back in before the service can start.'
fi
