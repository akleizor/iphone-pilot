# Performance: how flow and fill spend their time

Measured on a recent iPhone; the usage lives in `../SKILL.md`.

## flow

`iph flow "GOAL"` runs a whole routine flow in one call; Jev makes every decision
and you are asked only when acting is unsafe. Per step:

| Stage | Cost | How |
|---|---|---|
| look | ~50 ms | latest frame from the runner's MJPEG stream (one connection, half scale) + warm Vision OCR (`bin/iph-ocr`, built from `iph/ocr.swift` on first use) |
| settle | 0.1–0.6 s | wait for three identical frames, so a push or scroll is never read mid-motion |
| decide | ~0.3 s | one TypeSafe Jev request: operation + every target head |
| act | ~0.35 s | XCTest tap/swipe |

When OCR cannot decide (switches, empty fields, icon-only controls) that one
step re-reads the accessibility tree (~0.85 s). `--tree` forces it;
`--value`/`--secret` imply it. Measured on the 13 (2026-10-10, 8/8 runs):
Battery 3.9–5.7 s, Display & Brightness 5.8 s, General → About 7.3–8.3 s,
General → Keyboard 10.2–11.8 s.

```bash
$I flow "Turn on Dark Mode in Settings and stop when it is on"
PW=$(your-password-tool get <item>) $I flow "Sign in to the server" --secret password=PW --value url=https://host:8788
```

- Jev only chooses among observed elements; it never invents coordinates or text.
  Text comes from `--value NAME=TEXT` / `--secret NAME=ENVVAR` (only the name is sent).
- Navigation proceeds on a plurality (op ≥ 0.3, target ≥ 0.4); toggles and typing
  need `--min-conf` (0.6). A confident tap on an item the goal names overrules a
  wavering "scroll". Search fields are hidden unless the goal says "search".
- It stops on BLOCKED, a coin-flip, four actions that leave the screen unchanged,
  or a target labelled delete/remove/erase/reset/sign out/buy/pay
  (`--allow-risky` to permit), printing the choices it saw.
- DONE is Jev's claim; verify with one `dump`/`wait`. `-v` prints what it saw;
  `--dry-run` decides one step without acting. Key: `$TYPESAFE_API_KEY`.


## dump

Measured on a recent iPhone (2026-10-14). `device.dump.ui` returns a per-part
`timing` in its json format and takes `skip` and `keyPaths`, so any part can be
measured, compared or left out on a device.

| Part | Cost | Default |
|---|---|---|
| app snapshot, all attributes (`XCUIElement.snapshot()`) | 160–570 ms | fallback only |
| app snapshot, the 10 attributes the dump reads | 95–310 ms | always |
| status bar (SpringBoard clock, signal) | ~255 ms | skipped |
| keyboard focus query | 30–60 ms (~350 ms in Safari) | when a text input is on screen |
| device frame (SpringBoard) | 50–80 ms | once, then while the app frame matches |

The reduced snapshot asks the accessibility client for only the attributes
behind ten snapshot key paths (mapped by `XCElementSnapshot`, as WebDriverAgent
does). On Settings, Settings > General, Safari with the keyboard up and iSH it
returned the same elements as the full snapshot, attribute for attribute.

`iph` probes the agent before each command with the orientation call (~25 ms,
on the runner's main actor) instead of `device.info` (~120 ms, a SpringBoard
query). Round trip over Wi-Fi: ~12 ms.

| Settings in front | before | now |
|---|---|---|
| runner dump | 600–700 ms | 210–240 ms |
| `iph dump`, end to end | ~0.75 s | 0.30–0.31 s |
| `iph tap LABEL` | ~0.9 s | 0.30–0.39 s |

## fill

`iph fill "FIELD=VALUE"... [--submit LABEL]` fills a form in one call. On a
runner with `device.io.fill` (source `runner/IOFill.swift`, compiled into the
13's DeviceKit build at `~/.cache/iphone-ops/devicekit`) the whole form is one
RPC: one snapshot finds every field, each is tapped and focus-checked on
device, text is synthesized, and one final snapshot proves every value.
3-field New Contact on the 13 (2026-10-10, 5/5): 2.5–2.9 s, was 12.3 s.
Older runners fall back to the step path below. Fields
match by label, placeholder, identifier or value. Each field's keyboard focus is
proven before typing and its content verified after (secure fields by length);
a field that will not take focus is refused, never typed blind. `VALUE` of
`$NAME` reads a secret from the environment. Use it for every sign-in form:
it needs no Jev and cannot put a password in the wrong field.


## Fastest path, in detail

Order of preference for any task, fastest first:

1. **Deep link** (`iph url`): `prefs:root=…` opens a Settings page in ~0.23 s
   (CoreDevice payload launch into the running app); `http(s)` and app URL
   schemes open through the agent. `iph flow` deep-links any Settings page its
   goal names and, when the goal just ends on that page, proves it by the
   navigation bar with `device.find` and returns without a model call.
2. **`iph fill`** for forms: one RPC, every value proven by one snapshot.
3. **`iph flow`** for the rest: Jev picks each tap from the screen stream.

Runner additions (13, source in `runner/`): `device.find` answers "is this on
screen" with one predicate query (~0.15 s vs 0.65 s for a dump);
`device.io.fill`; taps rotate by the UI's own aspect, not the accelerometer
(a phone lying sideways in a portrait app had every tap land on the wrong
control). `iph tap` dismisses a keyboard covering its target.

Generic suite on the 13 (2026-10-10, all PASS, one command each): Settings
→ About 1.1 s, Display 0.9 s, Safari URL 0.5 s, Clock → Stopwatch 2.5 s,
Contacts 3-field fill 2.8 s.


## Where the seconds go

An idle phone: a tap or swipe RPC is ~0.33 s (XCTest event synthesis), a push
animation ~0.4 s, a list drag 0.9 s, a Jev decision ~0.3 s, a stream read
~50 ms. `iph flow` overlaps the tap RPC with reading the next frame. While the
phone is in use (someone typing in another app) every RPC waits 2–4 s on the
foreground app's accessibility snapshot: drive the phone only when no one is.
