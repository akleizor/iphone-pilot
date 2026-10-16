# Connection and verification

Use `iph remote setup` once for its pinned skill-local Python dependency. Remote
operations require an explicit host and hardware UDID; no silent LAN fallback.
`IPH_AGENT_ADDR=host:port` sends ordinary UI commands to that agent only, without
launching a local runner if it fails. Use only after a verified remote runner start.

## Choose the transport

| Interface | Purpose | Success means |
|---|---|---|
| `iph device --reachable` | Native CoreDevice check | Native tunnel connected, not UI control |
| `iph doctor` | Local developer connection and agent RPC | Separate JSON capabilities; nonzero without agent |
| `iph remote doctor` | Explicit-host RemotePairing, device identity, screenshot and agent RPC | Separate capabilities; nonzero without agent |
| `iph remote screenshot PATH` | Screenshot via developer service, no DeviceKit required | A new image from the requested device |
| `iph remote run` | Bounded foreground XCTest session | Read runner result; independently verify agent and UI |

Never use ping, an open TCP port, an app icon, or an MDM acknowledgment as control
proof. `doctor` does not start a runner, install, pair, or alter settings. Remote
screenshots never overwrite existing files. Remote connections never silently fall
back to USB, local discovery, or another phone.

## Device identity

`IPH_DEVICE` resolves the CoreDevice UUID in `iph`; it also derives the agent port
and per-device state directory. Names live in `~/.config/iph/devices.json`.
`iph device` prints the resolved UUID.

RemotePairing uses the **hardware UDID**, supplied explicitly with `--udid`.
Obtain it from `devicectl device info details --device <CoreDevice UUID>`. Do not
confuse the two identifiers. The remote tunnel must return the requested UDID
before any screenshot or runner launch is attempted.

The native check opens a CoreDevice tunnel on demand and requires the exact JSON
state `connected`. The human device-list table and cached hardware details do not
prove reachability. A disconnected local route need not rule out a remote route.

## Setup and pairing

1. Establish Apple host trust locally using USB or an already trusted Wi-Fi path.
   Enable Developer Mode if required. Never reset or copy pairing credentials.
2. Run `iph remote setup`. This owns `.runtime/` under the skill and pins
   `pymobiledevice3==11.21.0`. It uses Homebrew Python 3.13+ with OpenSSL, overridable
   through `IPH_PYTHON`; it never installs into global Python or transfers secrets.
3. Run `iph remote pair --udid <hardware-UDID>`. It requires existing Apple trust,
   creates the additional RemotePairing record through the library, and prints the
   discovered pairing ports. Existing valid pairings are verified rather than reset.
4. Choose the phone's address from `tailscale status`, then pass that address and
   the discovered port explicitly. The default port is only a starting point;
   listeners can change after restart. Re-discover locally when necessary.

```bash
I=iph
IPH_DEVICE=<name> $I remote doctor --host <phone-tailnet-address> --udid <hardware-UDID> --pair-port <port>
IPH_DEVICE=<name> $I remote screenshot <new-output.png> --host <phone-tailnet-address> --udid <hardware-UDID> --pair-port <port>
IPH_DEVICE=<name> $I remote run --host <phone-tailnet-address> --udid <hardware-UDID> --pair-port <port> --seconds 120
```

`run` keeps the tunnel in its foreground process, with a maximum ten-minute
runner lifetime plus a setup/cleanup budget. Use a harness-tracked bounded job for
concurrent UI calls. Keep a long-lived runner under a supervisor you own (a LaunchAgent).
With the runner verified, use the same `iph` commands through
`IPH_AGENT_ADDR=<phone-tailnet-address>:<agent-port>`. This explicitly selects the
agent route and refuses rather than scanning the LAN on failure.

## What the tests established

A phone reached over Tailscale returned real screenshots while it was on Wi-Fi
(any Wi-Fi, including another phone's hotspot over a DERP relay). With Wi-Fi off,
fresh connections were refused although Tailscale ping still worked. Do not
promise cellular-only access.

## Building the runner

`scripts/build-runner.sh TEAM_ID [BUNDLE_PREFIX]` builds and signs the on-phone
agent: `mobile-next/devicekit-ios` at a pinned commit plus `runner/runner.patch`.
It installs nothing; `iph up` installs and starts it. The runner is an XCTest
bundle, so it needs Apple Development signing (an Apple ID in Xcode works); a
distribution profile installs it but iOS refuses the test session. A free team's
profiles last 7 days: rebuild before they lapse. A different signing team cannot
update an installed runner even when the bundle id matches.

## Verify before declaring remote control

1. Authenticate a fresh explicit-host connection and check the device UDID.
2. Capture a fresh screenshot; disclose the current Wi-Fi/cellular condition.
3. Start the installed XCTest runner, then prove its RPC responds.
4. Read the UI, perform one reversible authorized interaction, and verify the result.
5. Repeat after the actual network transition the user needs. Report each unverified
   stage separately. No bank login or scrape follows from a connectivity test alone.

## Agent lifecycle

`iph build` rebuilds `bin/iph` and `bin/iph-ocr`; it never signs or installs.
`iph up` checks the runner's profile and reports expired signing instead of
renewing it during a read or tap. The mobilecli dependency stays pinned in
`iph/go.mod`.

A successful signature check is not proof of installation, and an installed
runner is not proof it launches: qualify a runner only once its test session
starts and a real screen read and tap pass.
