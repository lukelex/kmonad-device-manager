# Development

## Run tests locally

Run the Go unit tests and Go-backed integration test suite with:

```sh
go test ./...
./tests/run.sh
./tests/install.sh
```

CI enforces a minimum Go statement coverage threshold and publishes the full
coverage report as a workflow artifact.

The integration suite uses a fake KMonad process and temporary device files to
cover configuration parsing, concurrent devices, duplicate-device failover,
symlink target replacement, disconnect cleanup, configuration removal, crash
recovery, missing-KMonad errors, locking, completions, and service syntax.

To exercise cgroup behavior against a real delegated cgroup v2 subtree, set
`KMONAD_TEST_CGROUP_ROOT` and run the Go tests. The test is skipped when that
environment variable is not configured.

To test a real user-systemd manager lifecycle, run:

```sh
KMONAD_TEST_SYSTEMD=1 ./tests/systemd-user.sh
```

This must be run from a logged-in Linux user session and is opt-in so normal
tests do not require a user systemd bus.

To smoke-test real Linux keyboard discovery, run:

```sh
KMONAD_TEST_REAL_INPUT=1 go test ./internal/platform
```

This is opt-in because normal tests use an injectable sysfs/evdev fixture and
must not require host input hardware or permissions.

## Build manually

```sh
CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' \
  -o kmonad-device-manager ./cmd/kmonad-device-manager
```

## Docker development environment

Build the development image and open an interactive shell:

```sh
docker compose run --rm dev bash
```

Run the complete containerized validation suite:

```sh
docker compose run --rm test
```

The Compose environment is intended for building and testing the manager. It
does not expose host keyboard devices or run the systemd user service.
