#!/usr/bin/env bash

set -Eeuo pipefail

repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tmp_dir="$(mktemp -d)"
trap 'chmod -R u+w "$tmp_dir" 2>/dev/null || true; rm -rf "$tmp_dir"' EXIT

if [ "${EUID}" -eq 0 ]; then
  printf '%s\n' 'SKIP: installer integration test requires an unprivileged user'
  exit 0
fi

work_dir="$tmp_dir/repository"
home_dir="$tmp_dir/home"
bin_dir="$tmp_dir/bin"
mkdir -p "$home_dir" "$bin_dir"
cp -a "$repo_dir/." "$work_dir"

real_go="$(command -v go)"
for command in getent id kmonad sudo systemctl; do
  cat > "$bin_dir/$command" <<'EOF'
#!/usr/bin/env bash
case "$(basename "$0")" in
  getent|sudo|systemctl) exit 0 ;;
  id) printf '%s\n' 'input uinput' ;;
  kmonad) exit 0 ;;
esac
EOF
  chmod +x "$bin_dir/$command"
done
cat > "$bin_dir/go" <<EOF
#!/usr/bin/env bash
exec "$real_go" "\$@"
EOF
chmod +x "$bin_dir/go"

HOME="$home_dir" \
XDG_CONFIG_HOME="$home_dir/.config" \
XDG_DATA_HOME="$home_dir/.local/share" \
GOMODCACHE="$tmp_dir/modcache" \
GOCACHE="$tmp_dir/gocache" \
KMONAD_DEVICE_MANAGER_VERSION=9.9.9 \
PATH="$bin_dir:$PATH" \
"$work_dir/install.sh"

manager="$home_dir/.local/bin/kmonad-device-manager"
[ -x "$manager" ] || { printf '%s\n' 'FAIL: installer did not create the manager binary' >&2; exit 1; }
[ "$("$manager" --version)" = 'kmonad-device-manager 9.9.9' ] \
  || { printf '%s\n' 'FAIL: installer did not inject the requested version' >&2; exit 1; }
[ -L "$home_dir/.config/systemd/user/kmonad-device-manager.service" ] \
  || { printf '%s\n' 'FAIL: installer did not link the user service' >&2; exit 1; }

printf '%s\n' 'PASS: installer source-build integration test'
