# Reliability TODO

Prioritized follow-up work for making KMonad Device Manager more reliable in
failure, recovery, and high-load scenarios.

## P0 — Correctness races

- [x] **Bind validation to the exact configuration that is launched**
  `reconcile` reads a configuration, runs `kmonad --dry-run`, and later starts
  the configuration by path. The file can change between validation and launch.
  Re-read or hash the configuration after dry-run and abort/retry when its
  signature changes. This must cover both initial starts and updates to a
  running configuration.

- [ ] **Make process termination safe against PID reuse**
  Process termination currently uses process-group signaling and falls back to
  signaling the numeric PID. A rapidly reused PID could theoretically receive
  the fallback signal. Use Linux `pidfd_open`/`pidfd_send_signal`, or use
  cgroup-based killing when cgroup isolation is enabled.

- [x] **Ensure dry-run children cannot outlive the manager**
  Managed KMonad processes use parent-death handling, but dry-run processes do
  not. If the manager is killed during validation, a dry-run child may remain.
  Add parent-death handling and bound the wait after forced termination.

- [ ] **Retry filesystem watches after missing directories appear**
  `refreshWatches` records a path as watched even when adding it fails because
  the directory does not exist. If the directory later appears, it is not
  watched until polling detects a change. Record a path only after a successful
  watcher registration and add a regression test.

## P1 — Service lifecycle

- [ ] **Tie the systemd watchdog to reconciliation progress**
  The watchdog heartbeat runs independently of the reconciliation loop. A
  blocked reconciliation can therefore continue sending heartbeats and appear
  healthy. Emit heartbeats only after main-loop progress, or have the watchdog
  monitor a progress timestamp.

- [ ] **Use one global shutdown deadline**
  Configurations are stopped sequentially. With many configurations, slow
  children can make shutdown exceed systemd's `TimeoutStopSec`. Stop processes
  concurrently while enforcing one global deadline.

- [ ] **Handle metrics-server failure explicitly**
  Errors returned by `http.Server.Serve` are currently discarded. Log listener
  failure and expose a health/failure metric, or restart the metrics listener.

- [ ] **Make status persistence observable and durable**
  Status-file read, write, and rename failures are silently ignored. Log or
  count these failures, sync the file before renaming it, and optionally sync
  the containing directory when crash recovery depends on the status file.

## P1 — Resource limits and isolation

- [ ] **Align systemd `TasksMax` with configuration capacity**
  `KMONAD_MAX_CONFIGS` defaults to 128 while the systemd service has
  `TasksMax=128` for the manager and all descendants. Increase the service
  limit or explicitly derive the limit from the maximum number of configurations.

- [ ] **Limit configuration file size**
  Configuration files are loaded with unbounded `os.ReadFile`. A malformed or
  unexpectedly large file can consume excessive memory. Add a configurable
  `KMONAD_MAX_CONFIG_BYTES` limit with a conservative default.

- [ ] **Make cgroup setup and cleanup transactional**
  Cgroup creation and limit setup can fail after partially changing the cgroup.
  Remove partial cgroups on failure, use `cgroup.kill` for forced termination
  where available, and verify that no processes remain before removing a
  cgroup.

## P2 — Recovery quality

- [ ] **Add jitter to retry backoff**
  Simultaneously failing configurations currently retry on synchronized
  schedules. Add bounded random jitter to spread retries and avoid retry storms.

- [ ] **Persist recovery context**
  Status persistence restores retry timing but not the last failure reason or
  last known-good configuration signature. Persist these fields to improve
  post-crash diagnostics and recovery decisions.

- [ ] **Use strict process identity checks**
  Ownership checks partly rely on substring matches in `/proc/<pid>/cmdline`.
  Parse exact argument vectors and verify the resolved executable identity where
  possible.

- [ ] **Test and clean up descendant processes**
  Extend the fake KMonad to spawn children, ignore signals, hang during
  validation, and exit while children remain. Assert that no manager-owned
  descendants survive shutdown or restart.

## P2 — Reliability testing

- [ ] **Inject configuration replacement during dry-run**
  Verify that a changed configuration is never launched without validating the
  exact bytes that will be used.

- [ ] **Test watcher directory disappearance and recreation**
  Remove and recreate configuration/device directories and verify that event
  watching resumes without relying solely on polling.

- [ ] **Test status persistence failures**
  Inject failures for status-file writes, renames, and directory access, then
  verify that the manager remains functional and reports the failure.

- [ ] **Test manager termination during validation**
  Kill the manager during a slow dry-run and verify that no dry-run or KMonad
  descendant remains.

- [ ] **Test PID-reuse and signaling races**
  Exercise process exit, replacement, and stop concurrently to verify that the
  manager never signals an unrelated process.

- [ ] **Test partial cgroup setup**
  Fail each cgroup file operation independently and verify cleanup, retry
  behavior, and absence of leaked processes or cgroup directories.

- [ ] **Run long-duration soak tests**
  Repeatedly connect/disconnect devices and create/delete/replace configurations
  under load. Check for leaked processes, stale states, increasing memory use,
  and watcher recovery failures.
