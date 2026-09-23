# macOS Backend Plan

## Status

**Not implemented or advertised.** The current Darwin platform descriptor
truthfully reports unavailable capabilities. This document scopes the native
backend required before that status can change.

## Accepted product decisions

The first supported macOS release will provide:

- macOS 14 (Sonoma) or newer on Apple Silicon and Intel;
- independent per-keyboard supervision, known-good recovery, and hotplug
  handling at Linux feature parity;
- USB and Bluetooth keyboard coverage;
- a privileged `launchd` LaunchDaemon for KMonad capture/output work;
- user-approved Input Monitoring guidance and diagnostics;
- the signed Karabiner-Elements DriverKit virtual-HID extension for output;
- a project Homebrew tap at
  [`lukelex/homebrew-tap`](https://github.com/lukelex/homebrew-tap), where the
  manager cask will depend on both `karabiner-elements` and a project-owned,
  source-pinned KMonad formula.

KMonad remains a one-way dependency: installing KMonad must not install the
manager.

## Upstream KMonad constraint

Upstream KMonad's documented macOS configuration is:

```clojure
(defcfg
  input  (iokit-name "product string")
  output (kext))
```

This is insufficient for the required manager contract:

1. `iokit-name` selects by product string, not a stable individual keyboard
   identity. It cannot distinguish two identical keyboards.
2. KMonad's macOS IOKit implementation holds capture state, the source-device
   map, event pipe, run loop, and output client in process-global C++ state.
   Multiple independent KMonad instances cannot safely own separate devices.
3. A normal macOS user service cannot seize keyboard input; upstream KMonad
   documents root execution and Input Monitoring permission as requirements.

Therefore, the manager must **not** enable Darwin capabilities merely because
KMonad parses a macOS `defcfg`. Doing so would violate per-device isolation,
duplicate-claim safety, and hotplug recovery.

### MAC-001 progress

The project fork at [`lukelex/kmonad`](https://github.com/lukelex/kmonad) now
contains [`7ef5b64`](https://github.com/lukelex/kmonad/commit/7ef5b64), an
initial implementation of `(iokit-registry-id N)`. It uses
`IORegistryEntryGetRegistryEntryID` to seize exactly one current IOKit keyboard
and updates `list-keyboards` to emit JSON Lines containing each current
registry ID and product name. Legacy `(iokit-name ...)` behavior is retained.

An IOKit registry ID is unique for the currently connected registry entry, not
a durable reconnect identity. It is deliberately an internal KMonad launch
selector: the manager must keep its own stable identity, rediscover a device
after hotplug, and render a newly validated snapshot with its current selector.
The fork must still pass macOS builds and the two-identical-keyboard hardware
proof before MAC-001 is complete.

## Required architecture

### 1. Forked or upstreamed KMonad macOS transport

Before the manager backend can launch KMonad, KMonad needs a macOS transport
that supplies all of the following:

- a stable, opaque-to-clients selector derived from IOKit registry identity and
  available USB/Bluetooth metadata, rather than product name alone;
- a selector that seizes exactly one intended keyboard interface;
- process-instance-local source, event queue, run loop, and output state;
- disconnect and reconnect reporting usable by manager reconciliation;
- a DriverKit output connection compatible with the maintained signed
  Karabiner-Elements virtual-HID extension.

The preferred path is an upstream KMonad contribution. If upstream cannot
accept it on the required timeline, maintain a narrowly scoped,
source-pinned fork in the project Homebrew tap. The fork must retain KMonad's
version reporting so manager compatibility checks can identify it.

### 2. Privileged service boundary

The existing Linux design is a same-user systemd service. On macOS, a user
client must not receive root privileges merely by connecting to the manager.
Use two processes:

- a per-user manager/controller that owns API state, managed revisions,
  validation requests, and GUI/CLI interaction;
- a root LaunchDaemon broker that alone starts, stops, and observes privileged
  KMonad instances after authenticating a narrow request from that user
  controller.

The broker protocol must bind requests to the requesting macOS user, authorize
only manager-owned immutable snapshot paths, bound process creation, and never
accept arbitrary command lines, paths, or configuration bytes. A GUI failure
must not affect the broker's unrelated running mappings.

The platform-independent authorization model is now covered by
`internal/platform/broker.go` and deterministic tests. It uses one-time,
expiring opaque snapshot grants bound to one user and configuration, tracks one
active launch per user/configuration pair, and permits stop only to that owner.
`internal/platform/broker_wire.go` also defines bounded JSON Lines frames that
reject unknown fields; its request type has no user, command, path, or content
field, and an authenticated transport supplies the user identity separately.
The same package connects validated frames to authorization and returns only
bounded, non-sensitive responses for well-formed rejected requests.
The model retains a bounded broker-private audit trail and supports an explicit,
owner-authorized cancellation request. `internal/platform/broker_snapshot.go`
stages a bounded, digested, owner-only broker-private copy from a trusted
platform verifier; its storage path is never represented in a controller frame.
Only a successful opaque start authorization can reopen that copy, which is
checked again for type, size, owner-only mode, and digest before launch.
Failed broker snapshot handoff aborts its matching start reservation, preventing
a failed launch from blocking a later valid revision for that configuration.
Neither is a broker implementation: MAC-002 still requires authenticated IPC,
the Darwin-specific verified reader handoff, child lifecycle, audit-log
persistence, and macOS integration coverage.

### 3. Darwin `platform.System` backend

Once the KMonad transport and broker exist, `internal/platform/darwin.go` must
replace the unavailable descriptor with a Darwin implementation that provides:

- IOKit discovery and stable identity, without exposing locator data through
  public API responses;
- availability and hotplug events for USB and Bluetooth interfaces;
- platform-owned KMonad `defcfg` rendering using the unique input selector and
  DriverKit output form;
- broker-backed child lifecycle, identity, stop, and recovery primitives;
- launchd status and permission/DriverKit diagnostics.

Core `internal/manager` code continues to depend only on `platform.System`.
No IOKit, `launchd`, or DriverKit branches belong in GUI code.

## Delivery milestones

| Milestone | Deliverable | Exit criteria |
|---|---|---|
| MAC-001 | KMonad transport proof | Two identical keyboards are independently selected, each instance survives the other's stop, and DriverKit output works on Apple Silicon and Intel. |
| MAC-002 | Privileged broker | Authenticated user-to-broker protocol, immutable-snapshot allowlist, ownership checks, cancellation, audit logs, and negative authorization tests. |
| MAC-003 | Darwin platform backend | Discovery, identity, rendering, process, availability, and diagnostic implementations with deterministic unit tests. |
| MAC-004 | Launchd/install integration | Signed/notarized installer, LaunchDaemon/LaunchAgent assets, permission guidance, Karabiner checks, uninstall, and Homebrew integration. |
| MAC-005 | End-to-end parity | USB/Bluetooth connect-disconnect recovery, duplicate prevention, independent mapping failure isolation, known-good rollback, API/CLI contract tests, and physical-hardware coverage on both CPU architectures. |
| MAC-006 | Publish capability | Turn Darwin capabilities on only after all preceding milestones pass; publish the tap's KMonad formula and manager cask together. |

## Packaging contract

The future manager cask will declare:

```ruby
depends_on cask: "karabiner-elements"
depends_on formula: "kmonad"
```

The KMonad formula is source-pinned and built with the DriverKit output path.
It must not silently build an input-only binary. The manager installer is
responsible for installing and removing its own signed privileged broker assets;
Homebrew dependencies do not bypass macOS user consent or DriverKit approval.

## Test matrix

Every native milestone requires automated coverage where possible and recorded
hardware integration results for:

- macOS 14+ on Apple Silicon and Intel;
- one built-in keyboard, one USB keyboard, two identical USB keyboards, and a
  Bluetooth keyboard;
- initial connection, disconnect, reconnect, sleep/wake, and manager restart;
- missing Input Monitoring consent, missing/inactive Karabiner DriverKit,
  unavailable KMonad, duplicate claims, and broker authorization failures.

The Linux backend and external `.kbd` supervision remain a required regression
suite for every macOS change.
