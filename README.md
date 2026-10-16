# iph

Drive a real iPhone from your Mac, over Wi-Fi, from an AI agent or a script.

`iph` reads the screen as a list of elements, taps, types, scrolls and toggles,
and can run a whole goal-driven flow in one call. It talks to an XCTest runner on
the phone, so it works with any app, including system Settings, with no
jailbreak and no app changes.

```console
$ iph launch com.apple.Preferences && iph tap General && iph dump | head -5
@e1 [NavigationBar] "General" #General
  @e2 [Button] "Settings" #BackButton
  @e3 [StaticText] "General"
    @e4 [StaticText] "General"
@e5 [Button] "General, Manage your overall setup and preferences for Device, …"
$ iph tap About
$ iph flow "In Settings, open Accessibility, then Display & Text Size"
 0 link 1437ms Accessibility -> prefs:root=ACCESSIBILITY
 1 ocr  read 1981ms jev 1587ms TAP         p=0.74  [13] Button "AA Display & Text Size" p=0.77
 2 ocr  read 1873ms jev 1152ms DONE        p=0.90
done: 1 action(s) in 6.6s (verify the outcome independently)
```

## What it does

- **Reads the screen** as `@eN` element references (`dump`, ~0.3 s), or as text
  with tap points (`ocr`) for anything the accessibility tree hides.
- **Acts** by label, reference or point: `tap`, `toggle`, `text`, `swipe`,
  `scrollto`, `drag`, `pinch`, hardware buttons, deep links.
- **Refuses instead of guessing**: a reference whose element moved, a target
  covered by a toolbar or keyboard, a label that matches twice.
- **Says what happened**: an action exits 0 when applied, 1 when it was not
  (safe to retry), and 3 when the request was sent but no answer came back
  (read the screen before retrying).
- **Replays without a model**: a finished `flow` prints the same run as one
  `iph batch` command.
- **Fills forms** in one call (`fill`), proving each field took its value.
- **Runs flows** (`flow "GOAL"`): each step reads the screen and asks
  [TypeSafe Jev](https://typesafe.ai) to choose the next element (0.3–1.6 s). It
  only chooses among what is on screen and stops on anything risky.
- **Runs scripts** (`batch`): many known steps in one call.

A routine Settings flow takes 4–12 s end to end on a recent iPhone
([measurements](references/performance.md)).

## Requirements

- A Mac with Xcode, and Go 1.26+.
- An iPhone with Developer Mode on, trusted by this Mac and on the same Wi-Fi.
- An Apple ID added in Xcode > Settings > Accounts. A free one works; its
  signing lasts 7 days, so the runner is rebuilt weekly.
- For `flow` only: a TypeSafe API key in `TYPESAFE_API_KEY`.

## Install

```bash
git clone https://github.com/akleizor/iphone-pilot && cd iphone-pilot
(cd iph && go build -o ../bin/iph .) && bin/iph build   # the iph binary + OCR helper
bin/iph setup                                           # phone, runner, start: each step once
```

`iph setup` finds the trusted iPhone, writes `~/.config/iph/devices.json`,
builds and signs the on-phone runner with the team of your Apple Development
certificate, starts it, and skips any step already done. With several phones
it lists them; pick one with `--device <UUID>`. `--team` overrides the team.

### Or ask your agent

```text
Install iphone-pilot from https://github.com/akleizor/iphone-pilot on this Mac so you can drive my iPhone.
Build it as the README says and run `bin/iph setup`. If it reports something
only I can do (sign in to Xcode, tap Trust, turn on Developer Mode, unlock the
phone), tell me exactly what and wait. Then run `bin/iph doctor` and, once the
agent answers, `bin/iph launch com.apple.Preferences && bin/iph dump`.
```

### The device file

`iph setup` writes it; edit it for more phones:

```json
{
  "default": "test",
  "devices": {
    "test": {"uuid": "11111111-2222-3333-4444-555555555555"}
  }
}
```

Optional per device: `"udid"` (the hardware UDID, for phones where xcodebuild
rejects the CoreDevice UUID) and `"addrs"` (fixed routes to the agent, such as a
Tailscale address, tried before a LAN scan). `IPH_DEVICE=<name or UUID>` picks a
phone per command.

## Use with an agent

`SKILL.md` is an agent skill: point Claude Code, Codex or any agent that reads
skills at it, and it learns the commands, the targeting rules and when to use
`flow`, `fill` or `batch`.

## Commands

| Command | Does |
|---|---|
| `dump` · `ocr` · `screenshot` | Read the screen: elements, text with tap points, an image |
| `tap` · `toggle` · `text` · `swipe` · `scrollto` | Act on a label, `@eN` or `x,y` |
| `launch` · `open` · `url` · `kill` · `apps` | Apps and deep links |
| `fill` · `flow` · `batch` | A form, a goal, or a known script in one call |
| `setup` · `up` · `down` · `status` · `doctor` · `keep` | Install, runner lifecycle and diagnosis |

The full list, targeting rules and flow options are in [SKILL.md](SKILL.md);
connection, remote access over Tailscale and the verification ladder are in
[references/connection.md](references/connection.md).

## How it works

`iph` builds on [mobilecli](https://github.com/mobile-next/mobilecli) and its
on-phone agent [devicekit-ios](https://github.com/mobile-next/devicekit-ios).
mobilecli needs USB; `iph` starts the same agent with `xcodebuild` over
CoreDevice (which works over Wi-Fi) and talks to its JSON-RPC server on the LAN.
`runner/runner.patch` adds a one-call form fill and an orientation fix to the
agent. OCR runs on the Mac with Apple's Vision framework.

## Limits

- One Mac drives a phone at a time; a runner session shows "Automation Running"
  on the phone while it lives.
- No cellular-only control: the phone must be on Wi-Fi.
- `flow` decides navigation, not content. It never invents text; values come
  from `--value` or `--secret` (only the name leaves the Mac).

## License

Apache-2.0 (see [LICENSE](LICENSE)). It depends on mobilecli and devicekit-ios,
which are under the Functional Source License 1.1 (Apache-2.0 future license):
free to use and modify, but not to build a competing commercial product on, until
each version turns Apache-2.0 two years after release. See [NOTICE](NOTICE).
