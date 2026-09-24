# Remaining acceptance test

## KeyboarDeer Linux hardware acceptance *(post-release)*

Run this test on a real Linux host with two physical evdev keyboards. It is the
remaining hardware-level validation for the manager/API integration; container
tests and fake keyboards do not replace it.

### Preconditions

1. Build and install the exact manager revision under test.
2. Start the systemd user service and confirm it is healthy:

   ```sh
   kmonad-device-manager manager get --json
   ```

3. Connect two distinct physical keyboards that the operator can identify by
   sight. Do not use a manager-owned virtual output as either test keyboard.
4. Ensure the operator can read the two `/dev/input/event*` nodes.
5. Have a GUI/API client available that can connect, disconnect, reconnect, and
   retry a request with the same idempotency key.
6. Record the manager version, test date, keyboard descriptions, device IDs,
   configuration directory, and KMonad version. Save all JSON responses and
   service logs in a test report directory.

### Procedure

1. **Inventory and capability preflight**
   - Run `manager get --json` and confirm `device_input_scan` is advertised and
     available.
   - Run `devices --json` and record the two physical keyboard IDs.
   - Run `snapshot --json` and record the initial state revision.

2. **Independent identification**
   - Start identification for keyboard A:

     ```sh
     kmonad-device-manager identify start DEVICE_A --timeout 15 --json
     ```

   - Press a key only on keyboard A. Poll the returned operation until it
     succeeds and confirm its resource is `DEVICE_A`.
   - Repeat for keyboard B and confirm it produces `DEVICE_B`.
   - Confirm no identification operation attributes a keypress to the other
     keyboard.

3. **Input scan and probe agreement**
   - Run the automated real-device check for each keyboard:

     ```sh
     KMONAD_TEST_DEVICE_ACCEPTANCE=1 \
       ./tests/acceptance/inputscan-test04.sh DEVICE_A
     KMONAD_TEST_DEVICE_ACCEPTANCE=1 \
       ./tests/acceptance/inputscan-test04.sh DEVICE_B
     ```

   - Confirm each scan has namespace `kmonad-v1`, a valid digest, and the token
     set expected from that physical board.
   - Confirm the scan and identification results use the same device ID.
   - Confirm the ISO/ANSI probe and unmapped-key checks pass.

4. **One-device apply isolation**
   - Prepare a harmless test model targeting keyboard A and apply it using a
     unique idempotency key:

     ```sh
     kmonad-device-manager apply MODEL_A.json \
       --name 'Hardware acceptance A' \
       --idempotency-key acceptance-a-1 --json
     ```

   - Confirm the operation succeeds and the mapping works on keyboard A.
   - While applying or updating A, type on keyboard B and confirm B remains
     responsive and its mapping is unchanged.
   - Repeat with a model targeting B and confirm A remains unaffected.

5. **Hotplug behavior**
   - With both mappings running, unplug keyboard A and verify its device state
     becomes disconnected while keyboard B continues working.
   - Reconnect A and verify it returns under the expected identity, or under a
     new identity with the old record correctly invalidated.
   - Confirm the scan generation or digest changes after re-enumeration and
     that reconciliation restores only A's mapping.

6. **GUI disconnect and lost-response replay**
   - Start a configuration mutation through the GUI/API with a unique
     idempotency key.
   - Interrupt the client connection after submission but before receiving the
     response.
   - Reconnect and retry the exact same request with the same key.
   - Confirm the manager returns the original operation and does not create a
     duplicate configuration, process, revision, or event sequence.
   - Retry with the same key but different parameters and confirm it is
     rejected as an idempotency conflict.

7. **Known-good rollback**
   - Establish and record a known-good mapping for keyboard A, including its
     configuration revision and active process state.
   - Submit an update that passes the request path but fails activation or
     health confirmation in a controlled, reversible way.
   - Confirm the update is rejected or rolled back, the known-good revision is
     retained, and keyboard A returns to its prior working mapping.
   - Confirm keyboard B remains running throughout the failed update.

8. **Headless supervision and restart recovery**
   - Disconnect all GUI/API clients and verify both mappings continue under the
     systemd user service.
   - Check service health, process state, and logs while no client is present.
   - Restart the manager service and verify it recovers only manager-owned
     processes/configurations, restores both mappings, and does not duplicate
     processes or lose known-good state.

### Pass criteria and report

The test passes only if both keyboards remain isolated through every step,
lost responses replay idempotently, failed activation preserves the known-good
mapping, hotplug evidence is invalidated, and supervision/recovery work with no
GUI/API client connected.

Attach the saved JSON responses, TEST-04 reports, service logs, operation IDs,
configuration revisions, and a short timeline of unplug/replug/restart events
to the acceptance report. Mark this TODO complete only after the report is
reviewed and the physical-device run passes.
