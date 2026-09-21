#!/usr/bin/env bash

set -Eeuo pipefail

if [ "${KMONAD_TEST_SYSTEMD:-}" != 1 ]; then
  printf '%s\n' 'SKIP: set KMONAD_TEST_SYSTEMD=1 to run the user-systemd integration test'
  exit 0
fi

repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tmp_dir="$(mktemp -d)"
unit="kmonad-device-manager-integration-$$"
trap 'systemctl --user stop "$unit" 2>/dev/null || true; rm -rf "$tmp_dir"' EXIT

systemctl --user show-environment >/dev/null
mkdir -p "$tmp_dir/config" "$tmp_dir/runtime" "$tmp_dir/bin"
cat > "$tmp_dir/config/keyboard.kbd" <<'EOF'
(defcfg
  input (device-file "/dev/null")
)
EOF
cat > "$tmp_dir/bin/kmonad" <<'EOF'
#!/usr/bin/env bash
trap 'exit 0' TERM INT
while true; do sleep 0.1; done
EOF
chmod +x "$tmp_dir/bin/kmonad"
CGO_ENABLED=0 go build -buildvcs=false -trimpath -o "$tmp_dir/kmonad-device-manager" \
  "$repo_dir/cmd/kmonad-device-manager"

systemd-run --user --unit "$unit" --collect --no-block \
  --setenv="KMONAD_CONFIG_DIR=$tmp_dir/config" \
  --setenv="KMONAD_COMMAND=$tmp_dir/bin/kmonad" \
  --setenv="XDG_RUNTIME_DIR=$tmp_dir/runtime" \
  "$tmp_dir/kmonad-device-manager"

for _ in {1..100}; do
  [ -s "$tmp_dir/runtime/status.json" ] && break
  sleep 0.05
done
[ -s "$tmp_dir/runtime/status.json" ] || { printf '%s\n' 'FAIL: manager did not publish status' >&2; exit 1; }
systemctl --user is-active --quiet "$unit"
systemctl --user stop "$unit"
for _ in {1..100}; do
  ! systemctl --user is-active --quiet "$unit" && break
  sleep 0.05
done
! systemctl --user is-active --quiet "$unit" || { printf '%s\n' 'FAIL: user service did not stop' >&2; exit 1; }

printf '%s\n' 'PASS: user-systemd manager lifecycle integration test'
