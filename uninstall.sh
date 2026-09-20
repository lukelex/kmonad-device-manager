#!/usr/bin/env bash

set -Eeuo pipefail

systemctl --user disable --now kmonad-device-manager.service 2>/dev/null || true
rm -f "$HOME/.config/systemd/user/kmonad-device-manager.service"
rm -f "$HOME/.local/bin/kmonad-device-manager"
rm -f "$HOME/.config/kmonad-device-manager/env"
rm -f "${XDG_DATA_HOME:-$HOME/.local/share}/bash-completion/completions/kmonad-device-manager"
rm -f "${XDG_DATA_HOME:-$HOME/.local/share}/zsh/site-functions/_kmonad-device-manager"
rm -f "${XDG_CONFIG_HOME:-$HOME/.config}/fish/completions/kmonad-device-manager.fish"
rmdir "$HOME/.config/kmonad-device-manager" 2>/dev/null || true
systemctl --user daemon-reload

printf '%s\n' 'Removed the user service, manager binary, and completion files. KMonad configs and system permissions were left intact.'
