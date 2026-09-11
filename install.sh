#!/usr/bin/env bash

set -Eeuo pipefail

repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
config_dir="${XDG_CONFIG_HOME:-$HOME/.config}/kmonad"
force=0
install_kmonad=0

usage() {
  cat <<'EOF'
Usage: ./install.sh [--config-dir PATH] [--install-kmonad] [--force]

  --config-dir PATH  Directory containing KMonad .kbd files.
  --install-kmonad   Install KMonad with pacman when it is unavailable.
  --force            Replace existing manager symlinks.
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

for command in sudo getent groupadd usermod install modprobe udevadm systemctl; do
  require_command "$command"
done

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

link_file "$repo_dir/bin/kmonad-device-manager" "$HOME/.local/bin/kmonad-device-manager"
link_file "$repo_dir/systemd/kmonad-device-manager.service" "$HOME/.config/systemd/user/kmonad-device-manager.service"

mkdir -p "$HOME/.config/kmonad-device-manager"
printf 'KMONAD_CONFIG_DIR=%s\n' "$config_dir" > "$HOME/.config/kmonad-device-manager/env"

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
