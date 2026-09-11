#!/usr/bin/env bash

set -Eeuo pipefail

systemctl --user disable --now kmonad-device-manager.service 2>/dev/null || true
rm -f "$HOME/.config/systemd/user/kmonad-device-manager.service"
rm -f "$HOME/.local/bin/kmonad-device-manager"
rm -f "$HOME/.config/kmonad-device-manager/env"
systemctl --user daemon-reload

printf '%s\n' 'Removed the user service and manager binary. KMonad configs and system permissions were left intact.'
