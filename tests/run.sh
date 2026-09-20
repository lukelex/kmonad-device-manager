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
if [ "${1:-}" = --dry-run ]; then
  [ "$(basename "$2")" != invalid.kbd ]
  exit
fi
printf '%s\n' "$1" >> "$KMONAD_TEST_LOG"
if [ "$(basename "$1")" = crash.kbd ]; then
  exit 1
fi
trap 'exit 0' TERM INT
while true; do sleep 0.1; done
EOF
chmod +x "$tmp_dir/bin/kmonad"

config_one="$config_dir/one.kbd"
config_two="$config_dir/two.kbd"
config_three="$config_dir/three.kbd"
config_duplicate="$config_dir/z-duplicate.kbd"
config_comment="$config_dir/comment.kbd"
config_crash="$config_dir/crash.kbd"
config_invalid="$tmp_dir/invalid.kbd"
device_one="$device_dir/one"
device_two="$device_dir/two"
device_three="$device_dir/three"
device_crash="$device_dir/crash"

ln -s /dev/null "$device_one"
ln -s /dev/zero "$device_two"
ln -s /dev/urandom "$device_crash"
write_config "$config_one" "$device_one"
write_config "$config_two" "$device_two"
write_config "$config_three" "$device_three"
write_config "$config_duplicate" "$device_two"
write_config "$config_crash" "$device_crash"
write_config "$config_invalid" "$device_one"
printf '; input (device-file "%s")\n' "$device_one" > "$config_comment"

export KMONAD_CONFIG_DIR="$config_dir"
export KMONAD_COMMAND="$tmp_dir/bin/kmonad"
export KMONAD_DEVICE_MANAGER_LIBRARY=1
export KMONAD_TEST_LOG="$log_file"
source "$repo_dir/bin/kmonad-device-manager"

assert_equals "$device_one" "$(device_file "$config_one")"
assert_equals "" "$(device_file "$config_comment")"
reconcile
wait_for_lines 3
assert_running "${pids[$config_one]}"
assert_running "${pids[$config_two]}"
[ -z "${pids[$config_three]:-}" ] || fail 'started a configuration for a missing device'
[ -z "${pids[$config_duplicate]:-}" ] || fail 'started a duplicate device configuration'

if start_config "$config_invalid" "$(date +%s)"; then
  fail 'started an invalid configuration'
fi
[ -n "${retry_after[$config_invalid]:-}" ] || fail 'did not back off an invalid configuration'

pid_one="${pids[$config_one]}"
rm "$device_one"
reconcile
assert_stopped "$pid_one"
[ -z "${pids[$config_one]:-}" ] || fail 'retained a disconnected configuration'

ln -s /dev/full "$device_three"
reconcile
wait_for_lines 4
assert_running "${pids[$config_three]}"

pid_three="${pids[$config_three]}"
rm "$device_three"
ln -s /dev/random "$device_three"
reconcile
wait_for_lines 5
assert_stopped "$pid_three"
assert_running "${pids[$config_three]}"

pid_two="${pids[$config_two]}"
rm "$config_two"
reconcile
assert_stopped "$pid_two"
[ -z "${pids[$config_two]:-}" ] || fail 'retained a removed configuration'
wait_for_lines 6
assert_running "${pids[$config_duplicate]}"

reconcile
wait_for_lines 6
sleep 0.1
reconcile
assert_equals 6 "$(wc -l < "$log_file")"
[ -n "${retry_after[$config_crash]:-}" ] || fail 'did not back off a crashed process'

if env -u KMONAD_COMMAND PATH=/nonexistent /usr/bin/bash "$repo_dir/bin/kmonad-device-manager" > "$tmp_dir/missing.out" 2>&1; then
  fail 'manager succeeded without KMonad'
else
  missing_status=$?
fi
assert_equals 127 "$missing_status"
grep -q 'KMonad is not installed or is not on PATH' "$tmp_dir/missing.out" \
  || fail 'missing KMonad error was not clear'

if env -u KMONAD_DEVICE_MANAGER_LIBRARY \
  KMONAD_DOCTOR_COLOR=always \
  KMONAD_CONFIG_DIR="$config_dir" \
  KMONAD_COMMAND="$tmp_dir/bin/kmonad" \
  /usr/bin/bash "$repo_dir/bin/kmonad-device-manager" --doctor > "$tmp_dir/doctor.out" 2>&1; then
  fail 'doctor reported a healthy state with known invalid test inputs'
else
  doctor_status=$?
fi
[ "$doctor_status" -ne 0 ] || fail 'doctor reported a healthy state with known invalid test inputs'
grep -q $'\033[32m\[ok\]' "$tmp_dir/doctor.out" \
  || fail 'doctor did not render successful checks in green'
grep -q $'\033[31m\[bad\]' "$tmp_dir/doctor.out" \
  || fail 'doctor did not render failed checks in red'
grep -q $'\033[33m\[wait\]' "$tmp_dir/doctor.out" \
  || fail 'doctor did not render unavailable devices in yellow'
grep -q 'Configuration directory:' "$tmp_dir/doctor.out" \
  || fail 'doctor did not report the configuration directory'

if env -u KMONAD_COMMAND -u KMONAD_DEVICE_MANAGER_LIBRARY \
  PATH=/nonexistent \
  /usr/bin/bash "$repo_dir/bin/kmonad-device-manager" --doctor > "$tmp_dir/doctor-missing.out" 2>&1; then
  fail 'doctor reported a healthy state without KMonad'
else
  doctor_missing_status=$?
fi
[ "$doctor_missing_status" -ne 0 ] || fail 'missing-KMonad doctor reported a healthy state'
grep -q 'KMonad: not found on PATH' "$tmp_dir/doctor-missing.out" \
  || fail 'doctor did not report missing KMonad'

cmp <("$repo_dir/bin/kmonad-device-manager" --completion bash) \
  "$repo_dir/completions/kmonad-device-manager.bash"
cmp <("$repo_dir/bin/kmonad-device-manager" --completion zsh) \
  "$repo_dir/completions/_kmonad-device-manager"
cmp <("$repo_dir/bin/kmonad-device-manager" --completion fish) \
  "$repo_dir/completions/kmonad-device-manager.fish"

package_dir="$tmp_dir/package"
install -Dm755 "$repo_dir/bin/kmonad-device-manager" \
  "$package_dir/usr/bin/kmonad-device-manager"
for completion in "$repo_dir"/completions/*; do
  install -Dm644 "$completion" \
    "$package_dir/usr/share/kmonad-device-manager/completions/${completion##*/}"
done
cmp <("$package_dir/usr/bin/kmonad-device-manager" --completion bash) \
  "$repo_dir/completions/kmonad-device-manager.bash"
cmp <("$package_dir/usr/bin/kmonad-device-manager" --completion zsh) \
  "$repo_dir/completions/_kmonad-device-manager"
cmp <("$package_dir/usr/bin/kmonad-device-manager" --completion fish) \
  "$repo_dir/completions/kmonad-device-manager.fish"

bash -c 'source "$1"; COMP_WORDS=(kmonad-device-manager --completion z); COMP_CWORD=2; _kmonad_device_manager; [ "${COMPREPLY[*]}" = zsh ]' \
  bash "$repo_dir/completions/kmonad-device-manager.bash" \
  || fail 'Bash completion did not suggest shell names'

if command -v zsh >/dev/null 2>&1; then
  zsh -fc 'fpath=("$1" $fpath); autoload -Uz compinit; compinit -D; whence -w _kmonad-device-manager | grep -q function' \
    zsh "$repo_dir/completions" \
    || fail 'Zsh completion did not load'
fi

/usr/bin/bash "$repo_dir/install.sh" --help > /dev/null
assert_equals 'kmonad-device-manager 0.2.0' "$($repo_dir/bin/kmonad-device-manager --version)"

if command -v systemd-analyze >/dev/null 2>&1; then
  systemd-analyze --user verify "$repo_dir/systemd/kmonad-device-manager.service"
fi
grep -qx 'ExecStart=/usr/bin/kmonad-device-manager' \
  "$repo_dir/packaging/arch/kmonad-device-manager.service" \
  || fail 'packaged service does not use the system binary'

printf '%s\n' 'PASS: kmonad-device-manager tests'
