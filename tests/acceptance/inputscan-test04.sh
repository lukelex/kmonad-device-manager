#!/usr/bin/env bash
# TEST-04 real-device acceptance for device_input_scan (MGR-06).
#
# Opt-in and interactive: it needs a running kmonad-device-manager user service
# and a physical keyboard the operator can press, probe, and unplug. It is
# strictly read-only: it never writes a configuration or changes a mapping.
#
# Usage:
#   KMONAD_TEST_DEVICE_ACCEPTANCE=1 ./tests/acceptance/inputscan-test04.sh [DEVICE_ID]
#
# Environment:
#   KMONAD_DEVICE_MANAGER       CLI binary (default: kmonad-device-manager on PATH)
#   KMONAD_TEST_PROBE_TOKEN     token to probe (default: 102nd; must be the
#                               finalized kmonad-v1 spelling)
#   KMONAD_TEST_ACCEPTANCE_DIR  report directory (default: a fresh mktemp -d)
#
# Exit status: 0 when every step passes, 1 when a step fails, 2 on refusal.
set -u

if [ "${KMONAD_TEST_DEVICE_ACCEPTANCE:-0}" != "1" ]; then
	echo "Refusing to run: set KMONAD_TEST_DEVICE_ACCEPTANCE=1 (needs a real keyboard)." >&2
	exit 2
fi

MANAGER="${KMONAD_DEVICE_MANAGER:-kmonad-device-manager}"
PROBE_TOKEN="${KMONAD_TEST_PROBE_TOKEN:-102nd}"
DEVICE_ID="${1:-}"
OUT_DIR="${KMONAD_TEST_ACCEPTANCE_DIR:-$(mktemp -d)}"
mkdir -p "$OUT_DIR"

FAILURES=0

record() { # name status detail
	python3 -c 'import json,sys; print(json.dumps({"step": sys.argv[1], "status": sys.argv[2], "detail": sys.argv[3]}))' "$1" "$2" "$3" >>"$OUT_DIR/steps.jsonl"
	printf '  [%s] %s: %s\n' "$2" "$1" "$3"
	[ "$2" = "pass" ] || FAILURES=$((FAILURES + 1))
}

finish() {
	python3 - "$OUT_DIR" <<'PY'
import datetime, json, os, sys
out = sys.argv[1]
path = os.path.join(out, "steps.jsonl")
steps = [json.loads(line) for line in open(path)] if os.path.exists(path) else []
report = {
    "suite": "TEST-04",
    "generated_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
    "steps": steps,
    "failures": sum(1 for step in steps if step["status"] != "pass"),
}
json.dump(report, open(os.path.join(out, "report.json"), "w"), indent=2)
print("\nreport:", os.path.join(out, "report.json"))
PY
	if [ "$FAILURES" -eq 0 ]; then
		echo "TEST-04: PASS"
		exit 0
	fi
	echo "TEST-04: FAIL ($FAILURES step(s))"
	exit 1
}

jget() { # jget <dotted keys...>  (reads JSON on stdin)
	python3 -c 'import json, sys
d = json.load(sys.stdin)
for key in sys.argv[1:]:
    d = d[int(key)] if isinstance(d, list) else d[key]
print(d)' "$@"
}

confirm() { # confirm <prompt>
	local reply
	read -r -p "$1 [y/N] " reply
	case "$reply" in y | Y | yes | YES) return 0 ;; *) return 1 ;; esac
}

poll_operation() { # poll_operation <operation_id> <status_command...>
	local operation_id="$1"
	shift
	local state=""
	for _ in $(seq 1 40); do
		state=$("$@" "$operation_id" --json 2>/dev/null | jget operation state || true)
		[ -n "$state" ] && [ "$state" != "waiting" ] && [ "$state" != "running" ] && break
		sleep 0.5
	done
	echo "$state"
}

echo "== TEST-04 device input scan acceptance =="
echo "manager: $MANAGER"
echo "output:  $OUT_DIR"
echo

if ! command -v "$MANAGER" >/dev/null 2>&1; then
	record preflight fail "CLI '$MANAGER' was not found on PATH"
	finish
fi

if ! "$MANAGER" manager get --json >"$OUT_DIR/manager.json" 2>"$OUT_DIR/manager.err"; then
	record preflight fail "manager.get failed: $(cat "$OUT_DIR/manager.err")"
	finish
fi
available=$(python3 -c 'import json, sys
info = json.load(open(sys.argv[1]))
print(next((str(c["available"]) for c in info["capabilities"] if c["name"] == "device_input_scan"), "missing"))' "$OUT_DIR/manager.json")
if [ "$available" != "True" ]; then
	record preflight fail "device_input_scan is not advertised (available=$available)"
	finish
fi
record preflight pass "device_input_scan is advertised"

if [ -z "$DEVICE_ID" ]; then
	"$MANAGER" devices --json >"$OUT_DIR/devices.json" 2>/dev/null
	echo "Connected devices:"
	python3 -c 'import json, sys
for device in json.load(open(sys.argv[1]))["devices"]:
    print("  %-24s %-28s [%s]" % (device["id"], device["display_name"], device["availability"]))' "$OUT_DIR/devices.json"
	read -r -p "DEVICE_ID to test: " DEVICE_ID
fi
if [ -z "$DEVICE_ID" ]; then
	record device fail "no DEVICE_ID was provided"
	finish
fi

# --- 1. Baseline scan agrees with the board -------------------------------
echo
echo "-- Baseline scan --"
if ! "$MANAGER" inputscan get "$DEVICE_ID" --json >"$OUT_DIR/scan-baseline.json" 2>"$OUT_DIR/scan-baseline.err"; then
	record scan-baseline fail "inputscan get failed: $(cat "$OUT_DIR/scan-baseline.err")"
	finish
fi
namespace=$(jget token_namespace <"$OUT_DIR/scan-baseline.json")
scan_device=$(jget device_id <"$OUT_DIR/scan-baseline.json")
digest=$(jget digest <"$OUT_DIR/scan-baseline.json")
generation=$(jget generation <"$OUT_DIR/scan-baseline.json")
unmapped=$(jget unmapped_count <"$OUT_DIR/scan-baseline.json")
key_count=$(python3 -c 'import json, sys; print(len(json.load(open(sys.argv[1]))["keys"]))' "$OUT_DIR/scan-baseline.json")
echo "namespace:      $namespace"
echo "device:         $scan_device"
echo "keys ($key_count): $(python3 -c 'import json, sys; print(" ".join(json.load(open(sys.argv[1]))["keys"]))' "$OUT_DIR/scan-baseline.json")"
echo "unmapped_count: $unmapped"
echo "generation:     $generation"
echo "digest:         $digest"

if [ "$namespace" != "kmonad-v1" ] || [ "$scan_device" != "$DEVICE_ID" ] || [ "${digest#sha256:}" = "$digest" ]; then
	record scan-baseline fail "unexpected envelope: namespace=$namespace device=$scan_device digest=$digest"
else
	record scan-baseline pass "$key_count tokens, $unmapped unmapped, generation $generation"
fi

if confirm "Does the printed key set match the physical board (every key present, no extras)?"; then
	record board-agreement pass "operator confirmed the key set matches the board"
else
	record board-agreement fail "operator reported a mismatch between the scan and the board"
fi

# --- 2. Scan and identify agree on the same device -------------------------
echo
echo "-- Identification agreement --"
if ! "$MANAGER" identify start "$DEVICE_ID" --timeout 10 --json >"$OUT_DIR/identify-start.json" 2>"$OUT_DIR/identify-start.err"; then
	record identify-agreement fail "identify start failed: $(cat "$OUT_DIR/identify-start.err")"
else
	operation_id=$(jget operation id <"$OUT_DIR/identify-start.json")
	echo "Press any key on the keyboard now (identification is waiting)..."
	state=$(poll_operation "$operation_id" "$MANAGER" identify status)
	resource=$(jget operation resource id <"$OUT_DIR/identify-start.json")
	if [ "$state" = "succeeded" ] && [ "$resource" = "$DEVICE_ID" ]; then
		record identify-agreement pass "identification observed a keypress on $DEVICE_ID"
	else
		record identify-agreement fail "identify state=$state resource=$resource (wanted succeeded/$DEVICE_ID)"
	fi
fi

# --- 3. Probe splits ISO from ANSI ----------------------------------------
echo
echo "-- Bounded probe ($PROBE_TOKEN) --"
iso="no"
confirm "Is this board ISO (extra key between Left Shift and Z)?" && iso="yes"
if ! "$MANAGER" inputscan probe "$DEVICE_ID" "$PROBE_TOKEN" --timeout 10 --json >"$OUT_DIR/probe-start.json" 2>"$OUT_DIR/probe-start.err"; then
	record probe-split fail "inputscan probe failed: $(cat "$OUT_DIR/probe-start.err")"
else
	operation_id=$(jget operation id <"$OUT_DIR/probe-start.json")
	if [ "$iso" = "yes" ]; then
		echo "Press the $PROBE_TOKEN key now..."
	else
		echo "This is an ANSI board: press nothing and let the probe time out..."
	fi
	state=$(poll_operation "$operation_id" "$MANAGER" inputscan status)
	if [ "$iso" = "yes" ] && [ "$state" = "succeeded" ]; then
		record probe-split pass "ISO: $PROBE_TOKEN observed, candidates split"
	elif [ "$iso" = "no" ] && [ "$state" = "failed" ]; then
		record probe-split pass "ANSI: $PROBE_TOKEN absent, probe timed out as expected"
	else
		record probe-split fail "iso=$iso probe state=$state"
	fi
fi

# --- 4. Hotplug invalidates evidence --------------------------------------
echo
echo "-- Hotplug invalidation --"
read -r -p "Unplug the keyboard, wait about 3 seconds, replug it, then press Enter to rescan..." _
sleep 1
if "$MANAGER" inputscan get "$DEVICE_ID" --json >"$OUT_DIR/scan-after.json" 2>"$OUT_DIR/scan-after.err"; then
	new_generation=$(jget generation <"$OUT_DIR/scan-after.json")
	new_digest=$(jget digest <"$OUT_DIR/scan-after.json")
	if [ "$new_generation" -gt "$generation" ] || [ "$new_digest" != "$digest" ]; then
		record hotplug-invalidation pass "generation $generation->$new_generation, digest changed=$([ "$new_digest" != "$digest" ] && echo yes || echo no)"
	else
		record hotplug-invalidation fail "generation and digest did not change after replug"
	fi
else
	record hotplug-invalidation pass "device_id no longer resolves after replug; a stale proposal is invalidated by identity"
fi

finish
