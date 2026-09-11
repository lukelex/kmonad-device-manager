#!/usr/bin/env bash

set -Eeuo pipefail

repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tmp_dir="$(mktemp -d)"
config_dir="$tmp_dir/config"
device_dir="$tmp_dir/devices"
log_file="$tmp_dir/kmonad.log"

cleanup_test() {
  if declare -p pids >/dev/null 2>&1; then
    cleanup || true
  fi
  rm -rf "$tmp_dir"
}
trap cleanup_test EXIT

fail() {
  printf 'FAIL: %s\n' "$1" >&2
  exit 1
}

assert_equals() {
  [ "$1" = "$2" ] || fail "expected '$1', got '$2'"
}

assert_running() {
  kill -0 "$1" 2>/dev/null || fail "process $1 is not running"
}

assert_stopped() {
  if kill -0 "$1" 2>/dev/null; then
    fail "process $1 is still running"
  fi
  return 0
}

wait_for_lines() {
  local expected="$1"
  local lines=0
  local attempt

  for attempt in {1..50}; do
    [ -f "$log_file" ] && lines="$(wc -l < "$log_file")"
    [ "$lines" -ge "$expected" ] && return
    sleep 0.02
  done
  fail "expected $expected KMonad launches, got $lines"
}

write_config() {
  local config="$1"
  local device="$2"
  printf '(defcfg\n  input (device-file "%s")\n)\n' "$device" > "$config"
}

mkdir -p "$config_dir" "$device_dir" "$tmp_dir/bin"
cat > "$tmp_dir/bin/kmonad" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$1" >> "$KMONAD_TEST_LOG"
trap 'exit 0' TERM INT
while true; do sleep 0.1; done
EOF
chmod +x "$tmp_dir/bin/kmonad"

config_one="$config_dir/one.kbd"
config_two="$config_dir/two.kbd"
config_three="$config_dir/three.kbd"
device_one="$device_dir/one"
device_two="$device_dir/two"
device_three="$device_dir/three"

touch "$device_one" "$device_two"
write_config "$config_one" "$device_one"
write_config "$config_two" "$device_two"
write_config "$config_three" "$device_three"

export KMONAD_CONFIG_DIR="$config_dir"
export KMONAD_COMMAND="$tmp_dir/bin/kmonad"
export KMONAD_DEVICE_MANAGER_LIBRARY=1
export KMONAD_TEST_LOG="$log_file"
source "$repo_dir/bin/kmonad-device-manager"

assert_equals "$device_one" "$(device_file "$config_one")"
reconcile
wait_for_lines 2
assert_running "${pids[$config_one]}"
assert_running "${pids[$config_two]}"
[ -z "${pids[$config_three]:-}" ] || fail 'started a configuration for a missing device'

pid_one="${pids[$config_one]}"
rm "$device_one"
reconcile
assert_stopped "$pid_one"
[ -z "${pids[$config_one]:-}" ] || fail 'retained a disconnected configuration'

touch "$device_three"
reconcile
wait_for_lines 3
assert_running "${pids[$config_three]}"

pid_two="${pids[$config_two]}"
rm "$config_two"
reconcile
assert_stopped "$pid_two"
[ -z "${pids[$config_two]:-}" ] || fail 'retained a removed configuration'

if env -u KMONAD_COMMAND PATH=/nonexistent /usr/bin/bash "$repo_dir/bin/kmonad-device-manager" > "$tmp_dir/missing.out" 2>&1; then
  fail 'manager succeeded without KMonad'
else
  missing_status=$?
fi
assert_equals 127 "$missing_status"
grep -q 'KMonad is not installed or is not on PATH' "$tmp_dir/missing.out" \
  || fail 'missing KMonad error was not clear'

/usr/bin/bash "$repo_dir/install.sh" --help > /dev/null

if command -v systemd-analyze >/dev/null 2>&1; then
  systemd-analyze --user verify "$repo_dir/systemd/kmonad-device-manager.service"
fi

printf '%s\n' 'PASS: kmonad-device-manager tests'
