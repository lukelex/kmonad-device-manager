# TEST-04 — real-device acceptance for `device_input_scan`

This is the human-in-the-loop acceptance run for MGR-06. It cannot run in CI:
it needs a running `kmonad-device-manager` user service and a physical keyboard
the operator can press and unplug. The automated part is
[`inputscan-test04.sh`](inputscan-test04.sh); this page is the checklist and the
record of what "pass" means.

## Why this exists

The Go tests and JSON Lines fixtures prove the manager's scan/probe contract in
the abstract. They cannot prove that a real board's `EVIOCGBIT(EV_KEY)` decode
agrees with the keys printed on the keycaps, or that the kernel changes the node
identity on a re-plug. TEST-04 closes that gap.

## Preconditions

- Linux with the manager's systemd user service running and healthy
  (`kmonad-device-manager manager get --json`).
- The operator's user can read `/dev/input/event*` (the usual `input` group
  membership).
- One physical keyboard is connected and is **not** a manager-owned virtual
  output.
- The manager advertises `device_input_scan` in `manager.get.capabilities`. An
  older manager that does not is itself a pass for the capability-absent path,
  but this run requires the capability.

## Run

```sh
KMONAD_TEST_DEVICE_ACCEPTANCE=1 ./tests/acceptance/inputscan-test04.sh [DEVICE_ID]
```

The script is strictly read-only: it never writes a configuration, never
changes a mapping, and restores any config it pauses for a probe. It writes a
JSON report and the raw CLI captures to a scratch directory
(`KMONAD_TEST_ACCEPTANCE_DIR`, default `mktemp -d`).

`KMONAD_TEST_PROBE_TOKEN` selects the token probed in step 3. The canonical
manager spelling for the ISO/102nd key is `102nd`.

## Steps and pass criteria

| # | Step | Pass criterion |
|---|------|----------------|
| 0 | Preflight | `device_input_scan` is advertised and available |
| 1 | Baseline scan | `token_namespace == "kmonad-v1"`, `device_id` matches, `digest` is `sha256:…` |
| 1b | Board agreement | the printed token set matches the physical board exactly |
| 2 | Identification agreement | `identify` observes a keypress on the *same* `device_id` |
| 3 | Probe splits ISO/ANSI | ISO: pressing `102nd` succeeds; ANSI: the probe times out |
| 4 | Hotplug invalidation | after re-plug, `generation` increases or `digest` changes, or the old `device_id` no longer resolves |

Step 4 accepts either invalidation signal on purpose: a board with a serial
keeps its `device_id` across a re-plug (so `generation` must advance), while a
topology-identified board may return under a new `device_id` (so the old
proposal is invalid because the device is gone).

## Manual spot checks

Record these alongside the script output:

- **Unmapped keys.** Count vendor/media keys on the board that have no KMonad
  token and confirm they appear only in `unmapped_count`, never in `keys`.
- **Unrelated mappings keep running.** While a probe is waiting on the tested
  board, confirm another configured keyboard still types normally.
- **No config writes.** `git status` in the watched configuration directory
  stays clean and `state.get` shows no new revision after the run.

## Known limits

- `102nd` is a real KMonad token, but it is **not** the only spelling: KMonad
  also accepts `102d`, `lsgt`, and `nubs` for the same key. The manager emits
  `102nd` as its canonical `kmonad-v1` spelling; GUI clients should normalize
  alternate catalog spellings through KMonad keycode identity before matching.
- A board that reuses the same `eventN` for a *different* device under the same
  `device_id` will not advance `generation`; the recomputed `digest` is the
  backstop. This is noted, not a test failure.
