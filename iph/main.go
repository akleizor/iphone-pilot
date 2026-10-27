// iph drives a real iPhone over Wi-Fi through mobilecli's devicekit agent.
//
// mobilecli reaches the agent only through a usbmuxd port-forward, so it needs
// USB. iph reuses mobilecli's own client and ref/text formatter as a library,
// but starts the agent with xcodebuild (CoreDevice, works over Wi-Fi) and talks
// to it at <phone-ip>:<port> directly.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mobile-next/mobilecli/devices/devicekit"
	"github.com/mobile-next/mobilecli/types"
)

// RPC timeouts. The client default is 10 s; these are shorter where a wedged
// agent (port open, not answering) would otherwise stall a recovery step.
const (
	probeTimeout     = 2 * time.Second  // liveness check in findAgent
	terminateTimeout = 5 * time.Second  // terminate before a fresh launch
	launchTimeout    = 10 * time.Second // activate can block behind a system alert
	settleTimeout    = 5 * time.Second  // toggle / rotate waiting for the UI to catch up
)

// until polls cond every `every` until it holds or timeout passes.
func until(timeout, every time.Duration, cond func() bool) bool {
	for deadline := time.Now().Add(timeout); ; time.Sleep(every) {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
	}
}

var (
	deviceUUID = device(env("IPH_DEVICE", cfg.Default))
	// Each device gets its own port so a LAN scan can never answer for the wrong phone.
	port        = env("IPH_PORT", strconv.Itoa(8101+int(crc32.ChecksumIEEE([]byte(deviceUUID))%800)))
	stateDir    = env("IPH_STATE", filepath.Join(os.Getenv("HOME"), ".cache", "iphone-ops"))
	devDir      = filepath.Join(stateDir, deviceUUID) // per-device runtime state; the agent build is shared
	ipFile      = filepath.Join(devDir, "agent-ip")
	pidFile     = filepath.Join(devDir, "agent.pid")
	keepPidFile = filepath.Join(devDir, "keep.pid") // an `iph keep` supervising this device
	logFile     = filepath.Join(devDir, "agent.log")
	products    = filepath.Join(stateDir, "devicekit", "dd", "Build", "Products") // shared agent build output
)

// reachable is a var only so a test can prove `device --reachable` consults it:
// with the real call inlined, deleting the refusal below left the suite green.
var reachable = deviceReachable

// detailsCmd asks CoreDevice about one device, writing its JSON record to out.
//
// It queries the device rather than `devicectl list devices`, which cannot
// answer this question in either of its forms:
//
//   - The human table prints a physical device's *hardware UDID* in the
//     Identifier column (00008150-000000000000001E) where iph holds its
//     CoreDevice UUID (22222222-3333-4444-5555-666666666666), so a textual
//     match between the two fails for every real phone. It only looked right on
//     a simulator, whose two IDs are one string. The old grep of that table
//     rejected a connected phone this way (2026-10-04).
//   - The list's own states are stale for this purpose: an awake, paired,
//     installable phone sits at tunnelState "disconnected" (human column
//     "available (paired)") until something opens a tunnel. A details query
//     opens one on demand, which is why it answers "connected" for a phone the
//     list had just called disconnected — the same device, minutes later.
func detailsCmd(uuid, out string) *exec.Cmd {
	return exec.Command("xcrun", "devicectl", "device", "info", "details",
		"--device", uuid, "--json-output", out)
}

// deviceReachable returns nil when CoreDevice can talk to uuid right now.
// Opening the tunnel costs a few seconds against a build measured in minutes.
func deviceReachable(uuid string) error {
	out, err := os.CreateTemp("", "iph-device-*.json")
	if err != nil {
		return err
	}
	out.Close()
	defer os.Remove(out.Name())
	if b, err := detailsCmd(uuid, out.Name()).CombinedOutput(); err != nil {
		// devicectl names the cause itself ("The specified device was not
		// found"); quoting it beats guessing from here.
		return fmt.Errorf("device %s: CoreDevice query failed\n%s", uuid, strings.TrimSpace(string(b)))
	}
	b, err := os.ReadFile(out.Name())
	if err != nil {
		return err
	}
	return reachableFrom(uuid, b)
}

// reachableFrom decides from a details record whether the device is usable.
//
// A zero exit is NOT that answer: devicectl answers from this Mac's pairing
// record for a device that is nowhere near it, and reported success for an
// iPad Air 3 that had been away for months. Only its tunnelState does, and it
// is compared whole — the old check tested the human row for the substring
// `available`, which "unavailable" satisfies.
func reachableFrom(uuid string, detailsJSON []byte) error {
	var res struct {
		Result struct {
			Identifier           string `json:"identifier"`
			ConnectionProperties struct {
				TunnelState string `json:"tunnelState"`
			} `json:"connectionProperties"`
		} `json:"result"`
	}
	if err := json.Unmarshal(detailsJSON, &res); err != nil {
		return fmt.Errorf("devicectl device info details %s: %w", uuid, err)
	}
	if state := res.Result.ConnectionProperties.TunnelState; state != "connected" {
		if state == "" {
			state = "unreported"
		}
		// Report only what CoreDevice said. A failed tunnel does not say where
		// the phone is: agents turned this into "your phone is off Wi-Fi" while
		// it sat unlocked on the home network.
		return fmt.Errorf("device %s not reachable (CoreDevice tunnel %s)", uuid, state)
	}
	return nil
}

const usage = `iph — iPhone over Wi-Fi (devicekit agent)

<t> target = @eN (from the latest dump) | "Label" | x,y
  Refs are checked against the dump that issued them; labels match exactly,
  then by unique substring. Touches refuse targets hidden under an overlay.

setup    setup [--device UUID] [--team TEAM_ID]: config, runner build and start, skipping what is done
agent    up | status | down | doctor | keep (foreground: keep the runner warm) | build
remote   remote setup | pair | doctor | screenshot PATH | run [--seconds N]
         remote commands: --host HOST --udid HARDWARE_UDID --pair-port PORT
form     fill "FIELD=VALUE"... [--submit LABEL]   VALUE "$ENV" reads a secret from the environment;
         each field's focus is proven before typing (no model involved)
         --handback (fill or flow): return to the app that was in front and end
         automation the moment the work finishes. Use it on a phone someone is using.
goal     flow "GOAL" [--value NAME=TEXT] [--secret NAME=ENVVAR] [--steps N] [--min-conf P] [--dry-run]
         Jev (TypeSafe) picks each next tap/toggle/field from the live screen, ~0.5 s a step;
         stops with the screen on low confidence or BLOCKED. Key: $TYPESAFE_API_KEY.
read     dump [--json] | ocr | app | screenshot [--full] <file> | clipboard
         wait <label> [secs]          until it appears (default 10s)
         wait --gone <label> [secs]   until it disappears
         scrollto <label>             slow-scroll until it sits clear of bars; prints its ref
touch    tap <t> | doubletap <t> | longpress <t> [ms] | toggle <t>
         drag <t> <t> [ms] | pinch <in|out> [<t>] [distance]
         swipe <up|down|left|right|x1,y1,x2,y2> [ms]   direction = finger movement
type     text <string> | clear [<t>] | keys <combo>... (e.g. command+a backspace return)
         button <HOME|LOCK|VOLUME_UP|VOLUME_DOWN|ENTER>
batch    batch <step>...  a whole flow in one call, one liveness probe for all of it
         tap:L dbl:L press:L[:ms] drag:A>B togg:L swipe:dir[:ms] text:S
         key:return sleep:ms wait:L[:secs] gone:L[:secs] open:BUNDLE url:URL dump
         Separate steps: each acts on a fresh screen read (same as single commands).
         Join with '+' to share one read when a step does not change the screen.
         e.g. batch tap:General + tap:About      (one read, two taps)
              batch open:com.apple.Preferences tap:General sleep:800 tap:About
apps     apps [filter]  name -> bundle id of installed apps (incl. system)
         launch <bundle> (fresh) | open <bundle> (resume) | kill <bundle> | url <url>
device   clipboard <text> | rotate [portrait|landscape] | appearance <light|dark>

env: IPH_DEVICE (sandbox | primary | CoreDevice UUID; default sandbox), IPH_PORT, IPH_STATE, IPH_AGENT_ADDR (explicit host:port; no LAN fallback),
     IPH_UNTRACKED=1 (do not stamp <state>/<device>/last-use; for background owners)`

// lastUse is touched by every phone command so a background owner (fortivpn-guard) can
// tell an agent is mid-task and keep its hands off. Owners set IPH_UNTRACKED=1 so
// their own calls do not count as use.
var lastUse = filepath.Join(devDir, "last-use")

func markUse(cmd string) {
	switch cmd {
	case "-h", "--help", "help", "device", "build":
		return
	}
	if os.Getenv("IPH_UNTRACKED") != "" || os.MkdirAll(filepath.Dir(lastUse), 0o755) != nil {
		return
	}
	now := time.Now()
	if os.Chtimes(lastUse, now, now) != nil {
		_ = os.WriteFile(lastUse, nil, 0o644)
	}
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println(usage)
		os.Exit(2)
	}
	markUse(os.Args[1])
	if err := run(os.Args[1], os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "iph:", err)
		if !actions[os.Args[1]] {
			os.Exit(1)
		}
		code, note := outcome(err)
		if note != "" {
			fmt.Fprintln(os.Stderr, "iph:", note)
		}
		os.Exit(code)
	}
}

func run(cmd string, args []string) error {
	switch cmd {
	case "doctor":
		return doctor(args)
	case "setup":
		return setup(args)
	case "remote":
		return remoteCommand(args)
	case "up":
		addr, err := up()
		if err == nil {
			fmt.Println(addr)
		}
		return err
	case "status":
		addr, ok := findAgent()
		if !ok {
			return fmt.Errorf("UI agent not answering; phone connectivity not tested (run 'iph doctor')")
		}
		c := devicekit.NewDeviceKitClient(addr)
		ws, err := c.GetWindowSize()
		if err != nil {
			return err
		}
		app, err := c.GetActiveAppInfo()
		if err != nil {
			return err
		}
		fmt.Printf("%s  %dx%d@%dx  %s (%s)\n", addr, ws.ScreenSize.Width, ws.ScreenSize.Height, ws.Scale, app.BundleID, app.Name)
		return nil
	case "down":
		return down()
	case "keep":
		return keep()
	case "build":
		return build()
	case "device": // resolved CoreDevice UUID; scripts read it so aliases live only here
		if len(args) > 0 {
			if args[0] != "--reachable" {
				return fmt.Errorf("device takes only --reachable, got %q", args[0])
			}
			if err := reachable(deviceUUID); err != nil {
				return err
			}
		}
		fmt.Println(deviceUUID)
		return nil
	case "apps":
		return apps(args)
	case "-h", "--help", "help":
		fmt.Println(usage)
		return nil
	}

	c, err := client()
	if err != nil {
		return err
	}
	if cmd == "fill" || cmd == "flow" {
		args, hb := takeHandback(args)
		need(args, 1)
		f := fill
		if cmd == "flow" {
			f = doGoal
		}
		work := func() error { return f(c, args) }
		if hb {
			return withHandback(c, work)
		}
		return work()
	}
	if cmd == "batch" {
		need(args, 1)
		chains, err := parseSteps(args)
		if err != nil {
			return err
		}
		return batch(c, chains)
	}
	switch cmd {
	case "dump":
		els, err := dumpAndRemember(c)
		if err != nil {
			return err
		}
		if len(args) > 0 && args[0] == "--json" {
			return json.NewEncoder(os.Stdout).Encode(els)
		}
		fmt.Print(types.FormatText(els))
	case "tap", "doubletap":
		need(args, 1)
		x, y, err := point(c, args[0])
		if err != nil {
			return err
		}
		if cmd == "tap" {
			return c.Tap(x, y)
		}
		// The agent allows one press/release per finger per gesture, so a double
		// tap is two back-to-back gestures, well inside iOS's ~300 ms window.
		for i := 0; i < 2; i++ {
			if err := c.Gesture(taps(x, y)); err != nil {
				return err
			}
		}
		return nil
	case "longpress":
		need(args, 1)
		x, y, err := point(c, args[0])
		if err != nil {
			return err
		}
		return c.LongPress(x, y, intArg(args, 1, 1000))
	case "toggle":
		need(args, 1)
		return toggle(c, args[0])
	case "drag":
		need(args, 2)
		from, els, x1, y1, err := resolve(c, args[0])
		if err != nil {
			return err
		}
		if from != nil {
			if err := uncovered(els, from, x1, y1); err != nil {
				return err
			}
		} else if els, err = dump(c); err != nil {
			return err
		}
		_, x2, y2, err := resolveIn(els, args[1]) // destination may legitimately be covered
		if err != nil {
			return err
		}
		return c.Gesture(drag(x1, y1, x2, y2, intArg(args, 2, 600)))
	case "pinch":
		need(args, 1)
		return pinch(c, args)
	case "swipe":
		need(args, 1)
		x1, y1, x2, y2, err := swipeCoords(c, args[0])
		if err != nil {
			return err
		}
		return c.Swipe(x1, y1, x2, y2, intArg(args, 1, 0))
	case "text":
		need(args, 1)
		return c.SendKeys(strings.Join(args, " "))
	case "keys":
		need(args, 1)
		keys, err := combos(args)
		if err != nil {
			return err
		}
		return c.PressKeys(keys)
	case "clear":
		return clear(c, args)
	case "button":
		need(args, 1)
		return c.PressButton(strings.ToUpper(args[0]))
	case "wait":
		need(args, 1)
		return wait(c, args)
	case "scrollto":
		need(args, 1)
		ref, err := scrollTo(c, args[0])
		if err == nil {
			fmt.Println(ref)
		}
		return err
	case "screenshot":
		need(args, 1)
		full := args[0] == "--full"
		if full {
			need(args, 2)
			args = args[1:]
		}
		png, err := c.TakeScreenshot()
		if err != nil {
			return err
		}
		return saveScreenshot(args[0], png, full)
	case "ocr":
		// The text as the screen shows it. System alerts (an Apple Account
		// verification code) keep their text out of the accessibility tree.
		png, err := c.TakeScreenshot()
		if err != nil {
			return err
		}
		ocr, err := startOCR()
		if err != nil {
			return err
		}
		defer ocr.close()
		ws, err := c.GetWindowSize()
		if err != nil {
			return err
		}
		lines, err := ocr.read(png, float64(ws.Scale))
		if err != nil {
			return err
		}
		// the centre point, so text the tree hides (a sheet's "Log in") can be tapped as x,y
		for _, l := range lines {
			fmt.Printf("%s  @%d,%d\n", l.T, l.X+l.W/2, l.Y+l.H/2)
		}
		return nil
	case "launch", "open":
		need(args, 1)
		if cmd == "launch" {
			_, _ = c.CallRPCWithTimeout("device.apps.terminate", map[string]string{"bundleId": args[0]}, terminateTimeout)
		}
		return launch(c, args[0])
	case "kill":
		need(args, 1)
		_, err := c.CallRPC("device.apps.terminate", map[string]string{"bundleId": args[0]})
		return err
	case "url":
		need(args, 1)
		if err := openURL(c, args[0]); err != nil {
			return err
		}
		// a page or app still loading after the switch: return once two reads match (6 s at most)
		last := ""
		for deadline := time.Now().Add(6 * time.Second); time.Now().Before(deadline); {
			_, text, err := dumpText(c)
			if err != nil {
				return err
			}
			if text == last {
				return nil
			}
			last = text
			time.Sleep(200 * time.Millisecond)
		}
		return nil
	case "app":
		app, err := c.GetActiveAppInfo()
		if err != nil {
			return err
		}
		fmt.Printf("%s (%s) %s\n", app.BundleID, app.Name, app.ViewController)
	case "clipboard":
		if len(args) > 0 {
			return c.SetClipboard(strings.Join(args, " "))
		}
		text, err := c.GetClipboard()
		if err == nil {
			fmt.Println(text)
		}
		return err
	case "rotate":
		if len(args) == 0 {
			o, err := c.GetOrientation()
			if err == nil {
				fmt.Println(strings.ToLower(o))
			}
			return err
		}
		want := strings.ToLower(args[0])
		if err := c.SetOrientation(want); err != nil {
			return err
		}
		if until(settleTimeout, 250*time.Millisecond, func() bool { // the rotation animation takes ~2 s to report
			o, err := c.GetOrientation()
			return err == nil && strings.EqualFold(o, want)
		}) {
			return nil
		}
		return fmt.Errorf("device did not report %s within %s (orientation lock, or the app is portrait-only)", want, settleTimeout)
	case "appearance":
		need(args, 1)
		return c.SetAppearance(strings.ToLower(args[0]))
	default:
		return fmt.Errorf("unknown command %q\n\n%s", cmd, usage)
	}
	return nil
}

// agentAddr is the address client() connected to; `iph flow` opens its frame
// stream there.
var agentAddr string

func client() (*devicekit.DeviceKitClient, error) {
	addr, ok := findAgent()
	if !ok {
		var err error
		if addr, err = up(); err != nil {
			return nil, err
		}
	}
	agentAddr = addr
	return devicekit.NewDeviceKitClient(addr), nil
}

// ── batch: many steps in one call ──────────────────────────

type step struct {
	op, target, text string
	dur              int
}

// A chain is a group of steps joined by '+' that shares one screen reading.
// Steps not joined each re-read the screen first, so they see what the last one
// did — the same guarantee single commands give.
func parseSteps(args []string) ([][]step, error) {
	var chains [][]step
	joined := false // the previous token was '+': extend the current chain
	for _, a := range args {
		if a == "+" || a == "then" {
			if len(chains) == 0 {
				return nil, fmt.Errorf("'+' with no step before it")
			}
			joined = true
			continue
		}
		s, err := parseStep(a)
		if err != nil {
			return nil, err
		}
		if joined {
			i := len(chains) - 1
			chains[i] = append(chains[i], s)
		} else {
			chains = append(chains, []step{s})
		}
		joined = false
	}
	if len(chains) == 0 {
		return nil, fmt.Errorf("no steps")
	}
	return chains, nil
}

func parseStep(a string) (step, error) {
	verb, rest, _ := strings.Cut(a, ":")
	s := step{op: verb, target: rest}
	switch verb {
	case "tap", "dbl", "togg", "open", "url":
		if rest == "" {
			return s, fmt.Errorf("%s needs a value", verb)
		}
	case "press":
		if rest == "" {
			return s, fmt.Errorf("press needs a target")
		}
		s.dur = 1000 // default hold duration
		if lab, ms, ok := strings.Cut(rest, ":"); ok {
			if _, err := strconv.Atoi(ms); err == nil {
				s.target, s.dur = lab, intArg([]string{ms}, 0, 1000)
			} else {
				s.target = rest // not a duration suffix
			}
		} else {
			s.target = rest
		}
	case "swipe":
		s.dur = 300 // default swipe duration
		if rest == "" {
			s.target = "up"
		} else if dir, ms, ok := strings.Cut(rest, ":"); ok {
			// Parse duration from the right: "up:500" or coordinate-based swipes
			if _, err := strconv.Atoi(ms); err == nil {
				s.target, s.dur = dir, intArg([]string{ms}, 0, 300)
			} else {
				s.target = rest // not a duration, treat as full target
			}
		} else {
			s.target = rest
		}
	case "text":
		s.text = rest // everything after the first colon is the literal text
	case "dump", "dumpjson":
		// prints the screen this step ends on; needs no target
	case "key", "drag":
		if rest == "" {
			return s, fmt.Errorf("%s needs a value", verb)
		}
	case "sleep":
		s.dur = intArg([]string{rest}, 0, 0)
		if s.dur <= 0 {
			return s, fmt.Errorf("sleep needs milliseconds")
		}
	case "wait", "gone":
		lab, secs, ok := strings.Cut(rest, ":")
		if lab == "" {
			return s, fmt.Errorf("%s needs a label", verb)
		}
		s.target, s.dur = lab, 10
		if ok {
			s.dur = intArg([]string{secs}, 0, 10)
		}
	default:
		return s, fmt.Errorf("unknown step %q", verb)
	}
	return s, nil
}

func batch(c *devicekit.DeviceKitClient, chains [][]step) error {
	return runChains(c, chains)
}

func runChains(c *devicekit.DeviceKitClient, chains [][]step) error {
	for _, chain := range chains {
		if err := runChain(&c, chain); err != nil {
			return err
		}
	}
	return nil
}

func runChain(cp **devicekit.DeviceKitClient, chain []step) error {
	c := *cp
	// One reading serves the whole chain: steps joined by '+' are the caller's
	// assertion that they act on the same screen. A step that is not joined
	// starts its own chain, and so reads the screen as the last one left it.
	var els []types.ScreenElement
	loaded := false
	for i, s := range chain {
		last := i == len(chain)-1
		switch s.op {
		case "sleep":
			time.Sleep(time.Duration(s.dur) * time.Millisecond)
			continue
		case "open":
			if err := launch(c, s.target); err != nil {
				return err
			}
			// launch() may reconnect; refresh the client
			if rc, cerr := client(); cerr == nil {
				c = rc
				*cp = rc
			}
			loaded = false // the next step must read the app it just opened
			continue
		case "url":
			if err := openURL(c, s.target); err != nil {
				return err
			}
			loaded = false
			continue
		case "dump", "dumpjson":
			var err error
			if els, err = dump(c); err != nil {
				return err
			}
			remember(els) // refs it prints must match what later commands check
			loaded = true
			if s.op == "dumpjson" {
				if err := json.NewEncoder(os.Stdout).Encode(els); err != nil {
					return err
				}
			} else {
				fmt.Print(types.FormatText(els))
			}
			continue
		}
		if !loaded && needsEls(s.op) {
			var err error
			if els, err = dump(c); err != nil {
				return err
			}
			loaded = true
		}
		if err := applyStep(c, els, s); err != nil {
			return fmt.Errorf("%s:%s: %w", s.op, s.target, err)
		}
		if !last && needsScreen(s.op) {
			time.Sleep(settleGap)
		}
	}
	return nil
}

// needsEls reports whether a step needs the element tree to execute.
func needsEls(op string) bool {
	switch op {
	case "tap", "dbl", "press", "togg", "drag":
		return true
	}
	return false
}

// needsScreen reports whether a step touches the UI, and so wants a settle gap
// before the next step reads the result.
func needsScreen(op string) bool {
	switch op {
	case "sleep", "open", "url", "dump", "dumpjson":
		return false
	}
	return true
}

func applyStep(c *devicekit.DeviceKitClient, els []types.ScreenElement, s step) error {
	switch s.op {
	case "tap", "dbl", "press":
		el, x, y, err := resolveIn(els, s.target)
		if err != nil {
			return err
		}
		if el != nil {
			if err := uncovered(els, el, x, y); err != nil {
				return err
			}
		}
		switch s.op {
		case "tap":
			return c.Tap(x, y)
		case "dbl":
			// The agent allows one press/release per finger per gesture.
			for i := 0; i < 2; i++ {
				if err := c.Gesture(taps(x, y)); err != nil {
					return err
				}
			}
			return nil
		default:
			return c.LongPress(x, y, s.dur)
		}
	case "togg":
		el, _, _, err := resolveIn(els, s.target)
		if err != nil {
			return err
		}
		if el == nil || el.Type != "Switch" {
			return fmt.Errorf("%s is not a Switch; use tap", s.target)
		}
		r := el.Rect
		x, y := r.X+r.Width-30, r.Y+r.Height/2
		if err := uncovered(els, el, x, y); err != nil {
			return err
		}
		before := onOff(el)
		want := sigOf(el)
		if err := c.Tap(x, y); err != nil {
			return err
		}
		// Verify the toggle worked
		var now *types.ScreenElement
		if !until(settleTimeout, 200*time.Millisecond, func() bool {
			after, err := dump(c)
			if err != nil {
				return false
			}
			now = find(after, func(e *types.ScreenElement) bool { return sigOf(e) == want && e.Rect == el.Rect })
			return now != nil && onOff(now) != before
		}) {
			if now == nil {
				return fmt.Errorf("%s left the screen after the tap", want)
			}
			return fmt.Errorf("switch did not change")
		}
		return nil
	case "drag":
		a, b, ok := strings.Cut(s.target, ">")
		if !ok {
			return fmt.Errorf("drag wants A>B")
		}
		from, x1, y1, err := resolveIn(els, a)
		if err != nil {
			return err
		}
		if from != nil {
			if err := uncovered(els, from, x1, y1); err != nil {
				return err
			}
		}
		_, x2, y2, err := resolveIn(els, b)
		if err != nil {
			return err
		}
		if s.dur == 0 {
			s.dur = 600
		}
		return c.Gesture(drag(x1, y1, x2, y2, s.dur))
	case "swipe":
		x1, y1, x2, y2, err := swipeCoords(c, s.target)
		if err != nil {
			return err
		}
		if s.dur == 0 {
			s.dur = 300
		}
		return c.Swipe(x1, y1, x2, y2, s.dur)
	case "text":
		return c.SendKeys(s.text)
	case "key":
		combos, err := combos([]string{s.target})
		if err != nil {
			return err
		}
		return c.PressKeys(combos)
	case "wait", "gone":
		want := s.op == "wait"
		if !until(time.Duration(s.dur)*time.Second, 100*time.Millisecond, func() bool {
			fresh, err := dump(c)
			if err != nil {
				return false
			}
			_, lerr := resolveLabel(fresh, s.target)
			return (lerr == nil || errors.Is(lerr, errAmbiguous)) == want
		}) {
			state := "appear"
			if !want {
				state = "disappear"
			}
			return fmt.Errorf("%q did not %s within %ds", s.target, state, s.dur)
		}
		return nil
	}
	return fmt.Errorf("unhandled step %q", s.op)
}

// settleGap is how long a changed screen is given to catch up before the next
// step reads it. Dumps take ~1 s, so the read itself usually absorbs this.
const settleGap = 180 * time.Millisecond

// ── compound actions ───────────────────────────────────────

// toggle taps a switch's right edge — a centre tap on iOS switch rows is unreliable.
func toggle(c *devicekit.DeviceKitClient, target string) error {
	el, els, _, _, err := resolve(c, target)
	if err != nil {
		return err
	}
	if el == nil || el.Type != "Switch" {
		return fmt.Errorf("%s is not a Switch; use 'iph tap'", target)
	}
	r := el.Rect
	x, y := r.X+r.Width-30, r.Y+r.Height/2
	if err := uncovered(els, el, x, y); err != nil {
		return err
	}
	before, want := onOff(el), sigOf(el)
	if err := c.Tap(x, y); err != nil {
		return err
	}
	var now *types.ScreenElement
	until(settleTimeout, 200*time.Millisecond, func() bool { // the switch animates before it reports
		after, err := dump(c)
		if err != nil {
			return false
		}
		now = find(after, func(e *types.ScreenElement) bool { return sigOf(e) == want && e.Rect == el.Rect })
		return now != nil && onOff(now) != before
	})
	if now == nil {
		return fmt.Errorf("%s left the screen after the tap; check with 'iph dump'", want)
	}
	fmt.Printf("%s: %s -> %s\n", want, before, onOff(now))
	if before == onOff(now) {
		return fmt.Errorf("switch did not change")
	}
	return nil
}

func onOff(e *types.ScreenElement) string {
	if e.Checked != nil && *e.Checked {
		return "on"
	}
	return "off"
}

// clear empties the focused (or given) text field: select-all + delete, then
// per-character backspace if the field doesn't support select-all.
// focusedEl is the element holding keyboard focus, or nil.
// Safari can flag every element of a web page as focused, so a flagged text
// field wins over the first flagged element.
func focusedEl(els []types.ScreenElement) *types.ScreenElement {
	focused := func(e *types.ScreenElement) bool { return e.Focused != nil && *e.Focused }
	if f := find(els, func(e *types.ScreenElement) bool { return focused(e) && textEntry(e.Type) }); f != nil {
		return f
	}
	return find(els, focused)
}

func textEntry(t string) bool {
	return t == "TextField" || t == "SecureTextField" || t == "SearchField" || t == "TextView"
}

// backspaces is n delete presses (the agent rejects null modifiers).
func backspaces(n int) []devicekit.KeyCombo {
	keys := make([]devicekit.KeyCombo, n)
	for i := range keys {
		keys[i] = devicekit.KeyCombo{Key: "backspace", Modifiers: []string{}}
	}
	return keys
}

func clear(c *devicekit.DeviceKitClient, args []string) error {
	if len(args) > 0 {
		x, y, err := point(c, args[0])
		if err != nil {
			return err
		}
		if err := c.Tap(x, y); err != nil {
			return err
		}
	}
	value := func() (string, bool) {
		els, err := dump(c)
		if err != nil {
			return "", false
		}
		f := focusedEl(els)
		if f == nil {
			return "", false
		}
		v := deref(f.Value)
		if v == deref(f.Placeholder) {
			v = ""
		}
		return v, true
	}
	// select-all + delete is harmless on an empty field, so no pre-check dump.
	keys, err := combos([]string{"command+a", "backspace"})
	if err != nil {
		return err
	}
	_ = c.PressKeys(keys)
	v, ok := value()
	if !ok {
		return fmt.Errorf("no focused text field; tap one first or pass a target")
	}
	if v == "" {
		return nil
	}
	if err := c.PressKeys(backspaces(utf8.RuneCountInString(v))); err != nil {
		return err
	}
	if v, _ := value(); v != "" {
		return fmt.Errorf("field still contains %q", v)
	}
	return nil
}

// wait polls the screen until a label appears (or, with --gone, disappears).
func wait(c *devicekit.DeviceKitClient, args []string) error {
	gone := args[0] == "--gone"
	if gone {
		args = args[1:]
		need(args, 1)
	}
	label, secs := args[0], intArg(args, 1, 10)
	// A dump already takes ~1 s, so poll back-to-back.
	if until(time.Duration(secs)*time.Second, 100*time.Millisecond, func() bool {
		els, err := dump(c)
		if err != nil {
			return false
		}
		_, err = resolveLabel(els, label)
		return (err == nil || errors.Is(err, errAmbiguous)) != gone
	}) {
		return nil
	}
	state := "appear"
	if gone {
		state = "disappear"
	}
	return fmt.Errorf("%q did not %s within %ds", label, state, secs)
}

// apps lists installed apps via CoreDevice (works over Wi-Fi, no agent needed).
func apps(args []string) error {
	out, err := os.CreateTemp("", "iph-apps-*.json")
	if err != nil {
		return err
	}
	out.Close()
	defer os.Remove(out.Name())
	cmd := exec.Command("xcrun", "devicectl", "device", "info", "apps", "--device", deviceUUID,
		"--include-all-apps", "--json-output", out.Name())
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("devicectl: %v\n%s", err, b)
	}
	var res struct {
		Result struct {
			Apps []struct{ Name, BundleIdentifier string } `json:"apps"`
		} `json:"result"`
	}
	b, err := os.ReadFile(out.Name())
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, &res); err != nil {
		return err
	}
	filter := strings.ToLower(strings.Join(args, " "))
	for _, a := range res.Result.Apps {
		if filter == "" || strings.Contains(strings.ToLower(a.Name+" "+a.BundleIdentifier), filter) {
			fmt.Printf("%-32s %s\n", a.Name, a.BundleIdentifier)
		}
	}
	return nil
}

// launch activates an app, and on failure reports the system alert that
// blocked it (expired signing, "No Longer Available", permission prompts).
func launch(c *devicekit.DeviceKitClient, bundle string) error {
	_, err := c.CallRPCWithTimeout("device.apps.launch", map[string]string{"bundleId": bundle}, launchTimeout)
	if err == nil && until(settleTimeout, 250*time.Millisecond, func() bool {
		// the switch animation still reports the previous app for ~1 s
		app, aerr := c.GetActiveAppInfo()
		return aerr == nil && app.BundleID == bundle
	}) {
		return nil
	}
	// A refused launch can take the agent down with it; reconnect (restarting
	// it if needed) before reading what is on screen.
	if rc, cerr := client(); cerr == nil {
		c = rc
	}
	if els, derr := dump(c); derr == nil {
		var msg []string
		walk(els, nil, func(e *types.ScreenElement, anc []*types.ScreenElement) {
			for _, a := range append(anc, e) {
				if a.Type == "Alert" && (e.Type == "StaticText" || e.Type == "Button") {
					msg = append(msg, fmt.Sprintf("%s %s", e.Ref, sigOf(e)))
					return
				}
			}
		})
		if len(msg) > 0 {
			return fmt.Errorf("%s did not open; a system alert is showing: %s", bundle, strings.Join(msg, ", "))
		}
	}
	if err != nil {
		return fmt.Errorf("%s did not open: %w", bundle, err)
	}
	return fmt.Errorf("%s did not come to the foreground", bundle)
}

// scrollTo scrolls with slow drags (a flick's momentum skips rows) until the
// label is on screen in the middle band and not under a bar, reversing
// direction once the list stops moving.
func scrollTo(c *devicekit.DeviceKitClient, label string) (string, error) {
	ws, err := c.GetWindowSize()
	if err != nil {
		return "", err
	}
	w, h := ws.ScreenSize.Width, ws.ScreenSize.Height
	cx, step := w/2, h*4/10
	dir, prev, reversed, lastY := 1, "", false, -1 // dir 1: finger up, content moves up
	for i := 0; i < 20; i++ {
		els, err := dump(c)
		if err != nil {
			return "", err
		}
		var d int // finger travel: >0 moves content up
		el, err := resolveLabel(els, label)
		switch {
		case err == nil:
			_, y := center(el)
			clear := coveredBy(els, el, cx, y) == nil && y > 0 && y < h
			if clear && (y > h/5 && y < h*4/5 || y == lastY) { // middle band, or list end
				remember(els) // the returned ref must match what later commands check
				return el.Ref, nil
			}
			if y == lastY {
				return "", fmt.Errorf("%s %s is at the list edge under a bar; it cannot be scrolled clear", el.Ref, sigOf(el))
			}
			lastY = y
			d = max(-h*3/10, min(h*3/10, y-h/2)) // bring it to the middle
		case errors.Is(err, errAmbiguous):
			return "", err
		default:
			text := types.FormatText(els)
			if text == prev {
				if reversed {
					return "", fmt.Errorf("%q not found in either scroll direction", label)
				}
				dir, reversed = -dir, true
			}
			prev, d = text, dir*step
		}
		if err := c.Swipe(cx, h/2+d/2, cx, h/2-d/2, 900); err != nil {
			return "", err
		}
		time.Sleep(350 * time.Millisecond)
	}
	return "", fmt.Errorf("%q not found in either scroll direction", label)
}

func pinch(c *devicekit.DeviceKitClient, args []string) error {
	dir := args[0]
	if dir != "in" && dir != "out" {
		return fmt.Errorf("pinch direction must be in or out")
	}
	var x, y int
	if len(args) > 1 {
		var err error
		if x, y, err = point(c, args[1]); err != nil {
			return err
		}
	} else {
		ws, err := c.GetWindowSize()
		if err != nil {
			return err
		}
		x, y = ws.ScreenSize.Width/2, ws.ScreenSize.Height/2
	}
	dist := intArg(args, 2, 120)
	near, far := 20, 20+dist
	from, to := near, far
	if dir == "in" {
		from, to = far, near
	}
	var acts []devicekit.TapAction
	for finger, side := range []int{-1, 1} {
		acts = append(acts,
			devicekit.TapAction{Type: "pointerMove", X: x + side*from, Y: y, Button: finger},
			devicekit.TapAction{Type: "pointerDown", Button: finger},
			devicekit.TapAction{Type: "pause", Duration: 100, Button: finger},
			devicekit.TapAction{Type: "pointerMove", X: x + side*to, Y: y, Duration: 400, Button: finger},
			devicekit.TapAction{Type: "pointerUp", Button: finger},
		)
	}
	return c.Gesture(acts)
}

func taps(x, y int) []devicekit.TapAction {
	return []devicekit.TapAction{
		{Type: "pointerMove", X: x, Y: y},
		{Type: "pointerDown", Duration: 30},
		{Type: "pointerUp"},
	}
}

// drag holds first so iOS starts a drag (reorder, slider) rather than a scroll.
func drag(x1, y1, x2, y2, ms int) []devicekit.TapAction {
	return []devicekit.TapAction{
		{Type: "pointerMove", X: x1, Y: y1},
		{Type: "pointerDown"},
		{Type: "pause", Duration: 500},
		{Type: "pointerMove", X: x2, Y: y2, Duration: ms},
		{Type: "pointerUp"},
	}
}

// modifierAliases and combos copy mobilecli's commands.ParseKeyCombo verbatim in
// behaviour: importing that package would pull in its whole USB device stack.
var modifierAliases = map[string]string{
	"cmd": "command", "command": "command", "meta": "command",
	"ctrl": "control", "control": "control",
	"alt": "option", "opt": "option", "option": "option",
	"shift": "shift", "fn": "fn",
}

// combos parses "cmd+a", "ctrl+shift+z", "shift++" or "backspace". Modifiers is
// never nil: the agent rejects a null list.
func combos(args []string) ([]devicekit.KeyCombo, error) {
	var out []devicekit.KeyCombo
	for _, combo := range args {
		if combo == "" {
			return nil, fmt.Errorf("key combo cannot be empty")
		}
		parts := strings.Split(combo, "+")
		key, mods := parts[len(parts)-1], parts[:len(parts)-1]
		if key == "" { // trailing '+' means the key itself is '+'
			key = "+"
			if len(mods) > 0 {
				mods = mods[:len(mods)-1]
			}
		}
		modifiers := []string{}
		for _, m := range mods {
			canon, ok := modifierAliases[strings.ToLower(m)]
			if !ok {
				return nil, fmt.Errorf("unsupported modifier '%s' in key combo '%s'", m, combo)
			}
			modifiers = append(modifiers, canon)
		}
		out = append(out, devicekit.KeyCombo{Key: strings.ToLower(key), Modifiers: modifiers})
	}
	return out, nil
}

func swipeCoords(c *devicekit.DeviceKitClient, s string) (int, int, int, int, error) {
	var x1, y1, x2, y2 int
	if n, _ := fmt.Sscanf(s, "%d,%d,%d,%d", &x1, &y1, &x2, &y2); n == 4 {
		return x1, y1, x2, y2, nil
	}
	ws, err := c.GetWindowSize()
	if err != nil {
		return 0, 0, 0, 0, err
	}
	w, h := ws.ScreenSize.Width, ws.ScreenSize.Height
	cx, cy := w/2, h/2
	switch s {
	case "up":
		return cx, h * 7 / 10, cx, h * 3 / 10, nil
	case "down":
		return cx, h * 3 / 10, cx, h * 7 / 10, nil
	case "left":
		return w * 8 / 10, cy, w * 2 / 10, cy, nil
	case "right":
		return w * 2 / 10, cy, w * 8 / 10, cy, nil
	}
	return 0, 0, 0, 0, fmt.Errorf("want up|down|left|right or x1,y1,x2,y2, got %q", s)
}

// ── helpers ─────────────────────────────────────────────────

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// shotHeight is the long edge a screenshot is saved at. A full-resolution phone
// PNG is ~600 KB, and every one an agent reads stays in its session for good: it
// is re-sent with each request and makes a Remote Control session slow to open.
// 1000 px still shows layout, spacing and text clearly.
const shotHeight = "1000"

// saveScreenshot writes a JPEG scaled to shotHeight (macOS sips, no extra
// dependency), or the original PNG with full. A failed conversion falls back to
// the original rather than losing the capture.
func saveScreenshot(path string, png []byte, full bool) error {
	if full {
		return os.WriteFile(path, png, 0o644)
	}
	tmp, err := os.CreateTemp("", "iph-shot-*.png")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(png); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()
	out, err := exec.Command("sips", "-s", "format", "jpeg", "-s", "formatOptions", "70",
		"-Z", shotHeight, tmp.Name(), "--out", path).CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "iph: could not shrink the screenshot (%s); saved full size\n", strings.TrimSpace(string(out)))
		return os.WriteFile(path, png, 0o644)
	}
	return nil
}

func need(args []string, n int) {
	if len(args) < n {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
}

func intArg(args []string, i, def int) int {
	if len(args) > i {
		if v, err := strconv.Atoi(args[i]); err == nil {
			return v
		}
	}
	return def
}
