#!/usr/bin/env bash

set -Eeuo pipefail

repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tmp_dir="$(mktemp -d)"
config_dir="$tmp_dir/config"
device_dir="$tmp_dir/devices"
pid_dir="$tmp_dir/pids"
log_file="$tmp_dir/kmonad.log"
manager="$tmp_dir/kmonad-device-manager"
manager_pid=''

cleanup_test() {
  if [ -n "$manager_pid" ] && kill -0 "$manager_pid" 2>/dev/null; then
    kill -TERM "$manager_pid" 2>/dev/null || true
    wait "$manager_pid" 2>/dev/null || true
  fi
  for pid_file in "$pid_dir"/*.pid; do
    [ -f "$pid_file" ] || continue
    pid="$(cat "$pid_file")"
    kill -TERM "$pid" 2>/dev/null || true
  done
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
}

wait_for_lines() {
  local expected="$1"
  local _ lines=0
  for _ in {1..120}; do
    [ -f "$log_file" ] && lines="$(wc -l < "$log_file")"
    [ "$lines" -ge "$expected" ] && return
    sleep 0.05
  done
  fail "expected $expected KMonad launches, got $lines"
}

wait_for_stopped() {
  local pid="$1"
  local _
  for _ in {1..120}; do
    if ! kill -0 "$pid" 2>/dev/null; then
      return 0
    fi
    sleep 0.05
  done
  assert_stopped "$pid"
}

wait_for_file() {
  local path="$1"
  local _
  for _ in {1..120}; do
    [ -f "$path" ] && return 0
    sleep 0.05
  done
  fail "expected file was not created: $path"
}

wait_for_socket() {
  local path="$1"
  local _
  for _ in {1..120}; do
    [ -S "$path" ] && return 0
    sleep 0.05
  done
  fail "expected Unix socket was not created: $path"
}

wait_for_pid_change() {
  local path="$1"
  local old_pid="$2"
  local _ pid
  for _ in {1..120}; do
    if [ -f "$path" ]; then
      pid="$(cat "$path")"
      if [ "$pid" != "$old_pid" ] && kill -0 "$pid" 2>/dev/null; then
        return 0
      fi
    fi
    sleep 0.05
  done
  fail "process did not restart: $path"
}

pid_for() {
  cat "$pid_dir/$(basename "$1").pid"
}

write_config() {
  local config="$1"
  local device="$2"
  printf '(defcfg\n  input (device-file "%s")\n)\n' "$device" > "$config"
}

write_config_variant() {
  local config="$1"
  local device="$2"
  local iteration="$3"
  printf '; soak iteration %s\n(defcfg\n  input (device-file "%s")\n)\n' "$iteration" "$device" > "$config"
}

wait_for_no_kmonad_processes() {
  local _
  for _ in {1..120}; do
    if ! pgrep -f -- "$tmp_dir/bin/kmonad-test" >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.05
  done
  pgrep -af -- "$tmp_dir/bin/kmonad-test" >&2 || true
  fail 'manager-owned KMonad processes survived shutdown'
}

run_soak() {
  local iterations="${KMONAD_SOAK_ITERATIONS:-25}"
  if ! [[ "$iterations" =~ ^[1-9][0-9]*$ ]]; then
    fail "KMONAD_SOAK_ITERATIONS must be a positive integer"
  fi

  local config_ephemeral="$config_dir/ephemeral.kbd"
  local device_ephemeral="$device_dir/ephemeral"
  local iteration before_lines old_pid new_pid ephemeral_pid
  if [ ! -e "$device_one" ]; then
    ln -s /dev/null "$device_one"
    before_lines="$(wc -l < "$log_file")"
    wait_for_lines "$((before_lines + 1))"
  fi
  for ((iteration = 1; iteration <= iterations; iteration++)); do
    old_pid="$(pid_for "$config_one")"
    before_lines="$(wc -l < "$log_file")"
    write_config_variant "$config_one" "$device_one" "$iteration"
    wait_for_lines "$((before_lines + 1))"
    wait_for_pid_change "$pid_dir/one.kbd.pid" "$old_pid"
    new_pid="$(pid_for "$config_one")"
    [ "$new_pid" != "$old_pid" ] || fail "configuration replacement did not restart one.kbd in iteration $iteration"
    assert_running "$new_pid"

    if (( iteration % 3 == 0 )); then
      old_pid="$new_pid"
      rm -f "$device_one"
      wait_for_stopped "$old_pid"
      ln -s /dev/null "$device_one"
      before_lines="$(wc -l < "$log_file")"
      wait_for_lines "$((before_lines + 1))"
      wait_for_pid_change "$pid_dir/one.kbd.pid" "$old_pid"
      assert_running "$(pid_for "$config_one")"
    fi

    ln -s /dev/full "$device_ephemeral"
    before_lines="$(wc -l < "$log_file")"
    write_config_variant "$config_ephemeral" "$device_ephemeral" "$iteration"
    wait_for_lines "$((before_lines + 1))"
    wait_for_file "$pid_dir/ephemeral.kbd.pid"
    ephemeral_pid="$(pid_for "$config_ephemeral")"
    assert_running "$ephemeral_pid"
    rm -f "$config_ephemeral"
    wait_for_stopped "$ephemeral_pid"
    rm -f "$pid_dir/ephemeral.kbd.pid"
    rm -f "$device_ephemeral"

    "$manager" --status > "$tmp_dir/soak-status.out"
    grep -q '^one.kbd[[:space:]]\+running[[:space:]]' "$tmp_dir/soak-status.out" \
      || fail "status lost one.kbd during soak iteration $iteration"
  done
}

mkdir -p "$config_dir" "$device_dir" "$pid_dir" "$tmp_dir/bin" "$tmp_dir/run"
chmod 700 "$tmp_dir/run"
CGO_ENABLED=0 go build -buildvcs=false -trimpath -o "$manager" "$repo_dir/cmd/kmonad-device-manager"

cat > "$tmp_dir/bin/kmonad-test" <<'EOF'
#!/usr/bin/env bash
if [ "${1:-}" = --dry-run ]; then
  case "$(basename "$2")" in
    *invalid.kbd*) exit 1 ;;
  esac
  exit 0
fi
config="$1"
name="$(basename "$config")"
case "$name" in
  .kmonad-device-manager-snapshot-*.kbd-*)
    name="${name#.kmonad-device-manager-snapshot-}"
    name="${name%%.kbd-*}.kbd"
    ;;
esac
printf '%s\n' "${BASHPID:-$$}" > "$KMONAD_TEST_PID_DIR/$name.pid"
printf '%s\n' "$config" >> "$KMONAD_TEST_LOG"
if [[ "$name" = crash.kbd ]]; then
  exit 1
fi
trap 'rm -f "$KMONAD_TEST_PID_DIR/$name.pid"; exit 0' TERM INT
while true; do sleep 0.1; done
EOF
chmod +x "$tmp_dir/bin/kmonad-test"

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
export KMONAD_COMMAND="$tmp_dir/bin/kmonad-test"
export KMONAD_POLL_INTERVAL=1
export KMONAD_TEST_LOG="$log_file"
export KMONAD_TEST_PID_DIR="$pid_dir"
export XDG_RUNTIME_DIR="$tmp_dir/run"

"$manager" > "$tmp_dir/manager.out" 2>&1 &
manager_pid=$!
wait_for_lines 3
wait_for_socket "$tmp_dir/run/kmonad-device-manager/api.sock"

"$manager" --status > "$tmp_dir/status.out"
grep -q '^one.kbd[[:space:]]\+running[[:space:]]' "$tmp_dir/status.out" \
  || fail 'status did not report the running configuration'
"$manager" --status --json > "$tmp_dir/status.json"
grep -q '"configurations"' "$tmp_dir/status.json" \
  || fail 'JSON status did not contain configurations'
if "$manager" --status=json > "$tmp_dir/legacy-status.out" 2>&1; then
  fail 'removed --status=json spelling unexpectedly succeeded'
fi
grep -q -- '--status=json was removed; use --status --json' "$tmp_dir/legacy-status.out" \
  || fail 'removed --status=json spelling did not provide migration guidance'
"$manager" ps --json > "$tmp_dir/ps.json"
grep -q '"pid"' "$tmp_dir/ps.json" || fail 'JSON ps did not contain manager identity'
"$manager" --help --json > "$tmp_dir/help.json"
grep -q '"commands"' "$tmp_dir/help.json" || fail 'JSON help did not contain the command index'
"$manager" --version --json > "$tmp_dir/version.json"
grep -q '"version":"dev"' "$tmp_dir/version.json" || fail 'JSON version did not contain the build version'
"$manager" --completion bash --json > "$tmp_dir/completion.json"
grep -q '"shell":"bash"' "$tmp_dir/completion.json" || fail 'JSON completion did not identify its shell'

if "$manager" > "$tmp_dir/second-manager.out" 2>&1; then
  fail 'second manager instance unexpectedly started'
fi
grep -q 'another instance is already running' "$tmp_dir/second-manager.out" \
  || fail 'second manager did not report lock contention'
if "$manager" --json > "$tmp_dir/second-manager-json.out" 2>&1; then
  fail 'second JSON manager instance unexpectedly started'
fi
grep -q '"code":"lock_held"' "$tmp_dir/second-manager-json.out" \
  || fail 'JSON lock contention did not contain a structured error'

pid_one="$(pid_for "$config_one")"
pid_two="$(pid_for "$config_two")"
assert_running "$pid_one"
assert_running "$pid_two"
[ ! -f "$pid_dir/three.kbd.pid" ] || fail 'started a configuration for a missing device'
[ ! -f "$pid_dir/z-duplicate.kbd.pid" ] || fail 'started a duplicate device configuration'

rm "$device_one"
wait_for_stopped "$pid_one"

ln -s /dev/full "$device_three"
wait_for_lines 4
pid_three="$(pid_for "$config_three")"
assert_running "$pid_three"

rm "$device_three"
ln -s /dev/random "$device_three"
wait_for_lines 5
wait_for_stopped "$pid_three"
new_pid_three="$(pid_for "$config_three")"
[ "$new_pid_three" != "$pid_three" ] || fail 'did not restart after device replacement'
assert_running "$new_pid_three"

if [ "${KMONAD_SOAK:-0}" = 1 ]; then
  run_soak
fi

rm "$config_two"
wait_for_file "$pid_dir/z-duplicate.kbd.pid"
pid_duplicate="$(pid_for "$config_duplicate")"
assert_running "$pid_duplicate"

kill -KILL "$manager_pid"
wait "$manager_pid" 2>/dev/null || true
wait_for_stopped "$pid_duplicate"
wait_for_no_kmonad_processes
manager_pid=''

if env -u KMONAD_COMMAND PATH=/nonexistent "$manager" > "$tmp_dir/missing.out" 2>&1; then
  fail 'manager succeeded without KMonad'
else
  missing_status=$?
fi
assert_equals 127 "$missing_status"
grep -q 'KMonad is not installed or is not on PATH' "$tmp_dir/missing.out" \
  || fail 'missing KMonad error was not clear'

if KMONAD_DOCTOR_COLOR=always "$manager" --doctor > "$tmp_dir/doctor.out" 2>&1; then
  fail 'doctor reported a healthy state with known invalid test inputs'
else
  doctor_status=$?
fi
[ "$doctor_status" -ne 0 ] || fail 'doctor reported a healthy state with known invalid test inputs'
grep -q $'\033[32m\[ok\]' "$tmp_dir/doctor.out" || fail 'doctor did not render successful checks in green'
grep -q $'\033[31m\[bad\]' "$tmp_dir/doctor.out" || fail 'doctor did not render failed checks in red'
grep -q $'\033[33m\[wait\]' "$tmp_dir/doctor.out" || fail 'doctor did not render unavailable devices in yellow'
if KMONAD_DOCTOR_COLOR=always "$manager" --doctor --json > "$tmp_dir/doctor.json" 2>&1; then
  fail 'JSON doctor reported a healthy state with known invalid test inputs'
fi
grep -q '"command": "doctor"' "$tmp_dir/doctor.json" || fail 'JSON doctor did not identify its command'
grep -q '"severity": "error"' "$tmp_dir/doctor.json" || fail 'JSON doctor did not contain structured failures'
grep -q '"reason_code":' "$tmp_dir/doctor.json" || fail 'JSON doctor did not contain diagnostic reason codes'
grep -q '"remediation":' "$tmp_dir/doctor.json" || fail 'JSON doctor did not contain diagnostic remediation'
if grep -q $'\033\[' "$tmp_dir/doctor.json"; then
  fail 'JSON doctor contained ANSI color escapes'
fi

assert_equals 'kmonad-device-manager dev' "$($manager --version)"
cmp <("$manager" --completion bash) "$repo_dir/completions/kmonad-device-manager.bash"
cmp <("$manager" --completion zsh) "$repo_dir/completions/_kmonad-device-manager"
cmp <("$manager" --completion fish) "$repo_dir/completions/kmonad-device-manager.fish"

if command -v systemd-analyze >/dev/null 2>&1; then
  systemd-analyze --user verify "$repo_dir/systemd/kmonad-device-manager.service"
fi
grep -qx 'ExecStart=/usr/bin/kmonad-device-manager' \
  "$repo_dir/packaging/arch/kmonad-device-manager.service" \
  || fail 'packaged service does not use the system binary'

printf '%s\n' 'PASS: kmonad-device-manager tests'
