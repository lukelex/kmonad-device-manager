# KMonad Device Manager

## Toolchain and layout

- This is a single Go module (`github.com/lukelex/kmonad-device-manager`) and
  requires Go 1.27+. The executable entrypoint is
  `cmd/kmonad-device-manager`; its files are one `main` package, not separate
  commands.
- The manager is a Linux/systemd user service. Runtime behavior is primarily in
  `cmd/kmonad-device-manager/`; shell installers, service units, udev assets,
  and completions live at the repository root, `systemd/`, `system/`, and
  `completions/`. Completion files are embedded from `internal/completions/`.
- The manager validates each `.kbd` with `kmonad --dry-run`, then launches a
  validated temporary snapshot. Preserve this validation/snapshot lifecycle
  when changing supervision or reload behavior.

## Verification

- Run the normal Go checks with `go test -race ./...` and `go vet ./...`.
- Build the same static artifact used by installation with:
  `CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' ./cmd/kmonad-device-manager`.
- Run `./tests/run.sh` for the fake-KMonad integration suite and
  `./tests/install.sh` for the unprivileged installer test. The integration
  test builds its own binary and does not require a real KMonad/device.
- For the full CI-equivalent container check, run `docker compose run --rm
  test`; it also runs race tests, shellcheck, service validation, and both
  integration scripts. `KMONAD_SOAK=1 KMONAD_SOAK_ITERATIONS=25
  ./tests/run.sh` enables the longer lifecycle soak coverage.
- CI requires at least 55% Go statement coverage. Reproduce it with
  `go test -race -covermode=atomic -coverprofile=coverage.out ./...` followed
  by `./tests/check-coverage.sh coverage.out 55`.
- `KMONAD_TEST_SYSTEMD=1 ./tests/systemd-user.sh` is opt-in and requires a
  logged-in Linux user systemd session. A real delegated cgroup test is
  similarly opt-in via `KMONAD_TEST_CGROUP_ROOT`.

## Release hygiene

- Record user-visible changes in `CHANGELOG.md` under `Unreleased`, grouped as
  `Added`, `Changed`, `Fixed`, or `Security`. Releases need a dated version
  section matching the Git tag, with a fresh `Unreleased` section left above it.

## Public CLI documentation

- Every public `kmonad-device-manager` invocation must support meaningful
  `--json` output, including structured errors; do not add text-only commands.
- Any command, argument, or option addition/change must update all of these in
  the same change: the indexed GitHub Pages command reference at
  `docs/commands/index.md`, `docs/kmonad-device-manager.1`, the complete
  built-in `--help` metadata in `cmd/kmonad-device-manager/cli.go`, shell
  completions, and CLI/documentation tests.
- In all three documentation surfaces, include the full invocation, every
  argument and option with descriptions, JSON behavior, examples, and relevant
  exit statuses. A summary-only command listing is not sufficient.
