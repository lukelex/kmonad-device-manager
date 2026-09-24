# Development

All development commands in this guide run in Docker. Install Docker with
Compose support, clone the repository, and run commands from its root.

## Run tests locally

Run the complete containerized validation suite with:

```sh
docker compose run --rm test
```

CI enforces a minimum Go statement coverage threshold and publishes the full
coverage report as a workflow artifact.

The integration suite uses a fake KMonad process and temporary device files to
cover configuration parsing, concurrent devices, duplicate-device failover,
symlink target replacement, disconnect cleanup, configuration removal, crash
recovery, missing-KMonad errors, locking, completions, and service syntax.

To run only the Go tests in Docker:

```sh
docker compose run --rm dev go test ./...
```

To exercise cgroup behavior against a real delegated cgroup v2 subtree, pass
`KMONAD_TEST_CGROUP_ROOT` into the container:

```sh
docker compose run --rm \
  -e KMONAD_TEST_CGROUP_ROOT=/path/in/container dev \
  go test ./...
```

The cgroup test is skipped when that environment variable is not configured.

The complete container suite does not test a host user-systemd lifecycle. The
repository's systemd lifecycle test requires a logged-in host user session and
is therefore not available inside the development container.

Likewise, real keyboard discovery requires host input hardware and permissions;
the normal Docker tests use an injectable sysfs/evdev fixture instead.

## Build manually

Build the executable in the Docker development image:

```sh
docker compose run --rm dev \
  go build -buildvcs=false -trimpath -ldflags='-s -w' \
  -o kmonad-device-manager ./cmd/kmonad-device-manager
```

The output is written to the repository directory through the Compose volume.

## Interactive Docker environment

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
