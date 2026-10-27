---
name: iphone-pilot
description: "Operate a real iPhone from the Mac with the `iph` CLI: open apps, tap, toggle, type, scroll, read the screen, fill forms and run whole goal-driven flows (`iph flow`, `iph fill`). Use whenever something must be done or checked on a physical iPhone, or to debug its connection. Not for editing iOS code or building and shipping apps."
---

# iph

Operate real iPhones through `iph` — a thin Go CLI over mobilecli's devicekit
agent. mobilecli itself needs USB; `iph` starts the same agent via `xcodebuild`
(CoreDevice, works over Wi-Fi) and talks to it over the LAN directly, reusing
mobilecli's client and `@eN` formatter as a library.

```bash
I=<this skill folder>/bin/iph   # missing? (cd <this skill folder>/iph && go build -o ../bin/iph .)
$I launch com.apple.Preferences && $I scrollto "General" && $I tap "General"
$I dump                                        # read screen: @eN refs
$I tap @e12 && $I dump                         # act on a ref, re-read
$I toggle "Grid"                               # switch: prints on -> off
$I batch "open:com.apple.Preferences" "tap:General" "dump"   # a flow in one call
```

`iph` is the single entrypoint. Local UI operations may start an existing valid
runner; they never re-sign or reinstall it. Start with `iph doctor` when unsure.
A developer tunnel, screenshot, and working UI agent are different capabilities.
See `references/connection.md` for pairing and the explicit remote commands.

## Targets

`<t>` is `@eN` (from the latest `dump`), a **label**, or `x,y` points.

- **Label** — exact label/text/name/identifier first, then a unique
  case-insensitive substring. Invisible iOS characters (no-break spaces, bidi
  marks) are ignored, so type labels as they read. Several matches → error
  listing their refs; pick one.
- **`@eN`** — iph remembers what each ref was in the dump that issued it. If the
  screen changed, it follows the element when it is uniquely still there,
  otherwise refuses ("screen changed: @e16 was … now …") instead of touching
  whatever moved into that slot.
- **Covered targets are refused.** A target under a floating search field, tab
  bar, toolbar, nav bar or keyboard errors with the covering element — use
  `scrollto` to bring it clear.

## Commands

| Command | What |
|---|---|
| `doctor` | Read-only capability report; never installs or starts an agent. Nonzero when UI agent unavailable. |
| `remote setup/pair/doctor/screenshot/run` | Optional RemotePairing transport; see connection reference. |
| `setup` | Config, runner build and start in one call; skips what is done |
| `up` / `status` / `down` / `device` | Start agent · address, screen size (points@scale), foreground app · stop agent · resolved UUID (`device --reachable` also proves CoreDevice can reach it) |
| `dump [--json]` | Visible UI tree: `@e7 [Button] "Add" #id (checked)`; shows switch state, slider values, typed field text |
| `ocr` | The screen's text, one line each with its centre `@x,y`, read from a screenshot (~1 s). For text `dump` cannot see (an Apple Account code, a React Native sheet's "Log in"): tap it with `iph tap x,y` |
| `scrollto <label>` | Slow-scroll (no momentum) until the element is on screen and uncovered; prints its ref. Searches down, then up |
| `wait <label> [s]` / `wait --gone <label> [s]` | Poll until it appears / disappears (default 10 s) |
| `tap <t>` · `doubletap <t>` · `longpress <t> [ms]` | Touches (long press default 1000 ms — context menus) |
| `toggle <t>` | Flip a Switch by its right edge; prints `on -> off`, fails if unchanged |
| `drag <t> <t> [ms]` | Press-hold-move (sliders, reorder). Start on a slider's thumb |
| `pinch in\|out [<t>] [dist]` | Two-finger zoom out / in (default: screen centre) |
| `swipe up\|down\|left\|right [ms]` | Flick; direction = finger movement. `x1,y1,x2,y2 [ms]` for exact; ≥ 1000 ms has no momentum |
| `text "…"` · `clear [<t>]` · `keys <combo>…` | Type · empty focused/given field · keys like `command+a backspace return` |
| `button HOME\|LOCK\|VOLUME_UP\|VOLUME_DOWN\|ENTER` | Hardware buttons |
| `apps [filter]` | Installed apps: name → bundle ID (system + App Store) |
| `launch <bundle>` · `open <bundle>` · `kill <bundle>` | Fresh start · resume · terminate. A refused launch reports the blocking alert |
| `url <url>` · `app` | Open URL / deep link · foreground bundle + view controller |
| `clipboard [text]` · `rotate [portrait\|landscape]` · `appearance light\|dark` | Read/set clipboard · get/set orientation (waits for it) · theme |
| `screenshot [--full] <file>` | 1000 px JPEG (~60 KB); `--full` keeps the original PNG (~600 KB) |
| `batch <step>…` | A whole flow in one call. Each step acts on a fresh reading, as separate commands would; join steps with `+` to share one when the step doesn't change the screen. End with `dump` to see where you landed |

### Fastest path for any task

1. **Deep link**: `iph url prefs:root=…` or an app URL scheme (~0.2 s). `iph flow`
   deep-links a Settings page its goal names and confirms it by the navigation bar.
2. **`iph fill "FIELD=VALUE"… [--submit LABEL]`** for forms: one call, each field's
   focus proven before typing and its value after. `VALUE` of `$NAME` reads a secret
   from the environment. Use it for every sign-in form; it needs no model.
3. **`iph batch`** when you already know every step: one call, no model, ~0.5 s a
   step. Write the whole sequence yourself for an app you know; it beats asking Jev
   (~1.5 s and an occasional wrong pick per step).
4. **`iph flow "GOAL"`** for the rest: Jev picks each step from the screen stream.
   A finished flow is saved; the same goal next time replays it with no model and
   falls back to Jev if the screen differs. Keep the goal text identical to reuse it.

### flow: goal-driven (Jev)

```bash
$I flow "Turn on Dark Mode in Settings and stop when it is on"
PW=$(your-password-tool get <item>) $I flow "Sign in to the server" --secret password=PW --value url=https://host:8788
```

- Jev only picks observed elements; text comes from `--value NAME=TEXT` /
  `--secret NAME=ENVVAR` (only the name is sent).
- Navigation proceeds on a plurality (op ≥ 0.3, target ≥ 0.4); toggles and typing
  need `--min-conf` (0.6). Search fields are offered only when the goal says "search".
- It stops on BLOCKED, a coin-flip, four actions that leave the screen unchanged, or
  a risky target (`--allow-risky`), printing the choices it saw.
- **Always pass `--until "TEXT"`**: the text the finished screen shows ("Push on"). The flow ends the moment it appears, with no model call, and a DONE without it is a failure, not a success. With an app in front, "Settings" in the goal means that app's settings; say "iOS Settings" for the system app.
- DONE is Jev's claim; verify with one `dump`/`wait`. `-v` prints what it saw;
  `--dry-run` decides one step (a Settings deep link still opens); `--tree` forces
  the accessibility tree. Key: `$TYPESAFE_API_KEY` (typesafe.ai).

Stage costs and measured runs: `references/performance.md`.

### --handback: a phone someone is using

`iph flow … --handback` and `iph fill … --handback` remember the app in front,
and the moment the work ends (done, refused or failed) put that app back and
end the automation session, so the "Automation Running" overlay never
outlives the work and the person never waits on the agent to decide it is
done. A runner started by `iph up` or `iph keep` is stopped; one kept by a
service of your own is left running.

On a phone someone is actively using, always pass `--handback`.

### batch steps

`tap:L` `dbl:L` `press:L[:ms]` `drag:A>B` `togg:L` `swipe:dir[:ms]` `text:S`
`key:return` `sleep:ms` `wait:L[:secs]` `gone:L[:secs]` `open:BUNDLE` `url:URL` `dump`

Separate steps each read the screen fresh, exactly as separate commands do — that
is the safe default. Join steps with `+` only when the first one does **not**
change the screen (two toggles in one list, two fields on one form): the joined
steps share a single reading, so a screen that did move leaves the later steps
acting on coordinates from the old one.

```bash
$I batch "open:com.apple.Preferences" "sleep:1200" "tap:General" "tap:About" "dump"
$I batch "togg:Airplane Mode" + "togg:Wi-Fi"   # one screen, two switches, one read
```

## Results and exit codes

An action exits **0** when applied; **1** when it was not (iph or the runner
refused it, or it was never sent), safe to retry after a fresh `dump`; **3**
when it was sent but no answer came back: it may have happened, so read the
screen before retrying. A finished `flow` prints `replay: iph batch …`, the
same run with no model, and saves it for the same goal.

## Rules

- **Minimize tool calls.** Chain a whole task in one Bash call with `&&`; `&&` stops at the first refusal.
- **Prefer one `batch` to separate commands** (a 4-step flow is ~1.3× faster); use `&&` only when you must decide between steps.
- **Prefer labels; use refs for duplicates.** After a screen change, dump again before trusting a ref.
- **Dump shows only what's rendered.** Use `scrollto` for rows further down a list; `swipe` flicks and skips rows.
- **Switches:** always `toggle`, never `tap`.
- **Tree to act, screenshot to verify.** The tree omits pixels (images, badges, progress). Screenshot when a result contradicts expectations, before reporting.
- **Screenshots: few and small**: every image an agent reads stays in its context. `dump` for navigation, text and state; `ocr` for text the tree hides; a screenshot only for visual sign-off, `--full` only for pixel detail.
- **Coordinates** are logical points (`iph status`), never screenshot pixels.
- **System alerts and paywalls** are ordinary elements: tap the dismiss option (`Cancel`, `Not Now`), never a purchase or `Continue` you weren't asked for. Some keep their text out of the tree (Apple Account codes): read it with `iph ocr`.
- **Leave apps as found.** Restore anything you changed while testing (values, brightness, toggles, orientation).
- **Quiet output.** Print what you did and the result, not whole dumps.

## Device logs and crash reports

Apple's own tools read both over Wi-Fi; `iph` adds nothing on top. Use the phone's
hardware UDID (`xcrun devicectl list devices`), not its CoreDevice UUID.

```bash
# system log for the last few minutes (root; ~20 s for 2 min), then filter it
sudo log collect --device-udid <UDID> --last 5m --output /tmp/phone.logarchive
log show /tmp/phone.logarchive --style compact --predicate 'process == "MyApp" AND messageType == error'
# crash reports (no root, under a second)
xcrun devicectl device info files --device <UUID> --domain-type systemCrashLogs
xcrun devicectl device copy from --device <UUID> --domain-type systemCrashLogs --source <name>.ips --destination /tmp/<name>.ips
```

## Devices

`IPH_DEVICE` picks the phone: a name from `~/.config/iph/devices.json`
(`IPH_CONFIG` overrides) or a CoreDevice UUID; with neither, the file's
`default`. See the README for the file format. Each device gets its own agent
port (derived from its UUID) and state directory, so several phones can run at
once. Every command stamps `<state>/<device>/last-use`, for tools that wait
until a phone is idle; a background caller that must not count sets
`IPH_UNTRACKED=1`.

## Reporting phone state — facts only

Agents repeatedly told the user the phone was "offline", "not on home Wi-Fi" or
"locked" when it was unlocked on home Wi-Fi. The real failure was the UI runner.
Report only the layer that failed, worded as the tool printed it:

| Observation | Say |
|---|---|
| `doctor`: `developer_connection:true`, `agent:false` | "Phone connected; UI runner not running" + `agent_error` |
| `agent_error` mentions profile expired / Apple Development | "UI control unavailable: runner signing expired" |
| `developer_connection:false` | "CoreDevice tunnel `<state>`" — nothing about Wi-Fi, location or lock |
| xcodebuild log "because the device is locked" | the only evidence that justifies "locked" |

Never guess network, location or lock state from a failure. When unsure, run
`iph doctor` and quote it.

## Connectivity

Read `references/connection.md` for local setup, RemotePairing, Tailscale limitations,
and the verification ladder. Preserve failed capabilities in the final report;
never call the phone controllable merely because its VPN responds.
