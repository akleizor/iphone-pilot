package main

// `iph flow GOAL`: a fast inner loop for routine UI flows. Each cycle reads the
// screen once, asks TypeSafe's Jev (a non-generative choice model, ~0.4 s) for
// the next operation and target in ONE request, and executes it with the same
// guarded primitives as `tap` / `toggle`. The calling agent spends one turn on
// the whole flow instead of one turn per tap.
//
// Jev only chooses among what was observed: model output never becomes
// coordinates, labels or text. Text comes from caller-supplied values
// (--value / --secret), picked by index. Low confidence stops the loop with
// the screen table so the caller decides.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mobile-next/mobilecli/devices/devicekit"
	"github.com/mobile-next/mobilecli/types"
)

const jevURL = "https://api.typesafe.ai/v1/systemone"

var jevHTTP = &http.Client{Timeout: 20 * time.Second}

// actionable types Jev may target; everything else is context text.
var tapTypes = map[string]bool{
	"Button": true, "Cell": true, "Link": true, "MenuItem": true, "Tab": true,
	"SegmentedControl": true, "Icon": true,
}
var fieldTypes = map[string]bool{"TextField": true, "SecureTextField": true, "SearchField": true, "TextView": true}

type jevValue struct{ name, text string }

type doOpts struct {
	goal       string
	steps      int
	minConf    float64
	values     []jevValue
	dryRun     bool
	tree       bool
	allowRisky bool
	verbose    bool
	until      string // done only when this text is on screen
}

func parseDo(args []string) (doOpts, error) {
	o := doOpts{steps: 12, minConf: 0.6}
	for i := 0; i < len(args); i++ {
		a := args[i]
		next := func() (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("%s needs a value", a)
			}
			i++
			return args[i], nil
		}
		switch a {
		case "--steps", "--min-conf", "--value", "--secret", "--until":
			v, err := next()
			if err != nil {
				return o, err
			}
			switch a {
			case "--until":
				o.until = v
			case "--steps":
				o.steps, err = strconv.Atoi(v)
			case "--min-conf":
				o.minConf, err = strconv.ParseFloat(v, 64)
			case "--value", "--secret":
				name, rest, ok := strings.Cut(v, "=")
				if !ok || name == "" {
					return o, fmt.Errorf("%s wants NAME=%s", a, map[string]string{"--value": "TEXT", "--secret": "ENVVAR"}[a])
				}
				if a == "--secret" { // value read from the environment, never from argv
					env := rest
					if rest = os.Getenv(env); rest == "" {
						return o, fmt.Errorf("--secret %s: $%s is empty", name, env)
					}
				}
				o.values = append(o.values, jevValue{name, rest})
			}
			if err != nil {
				return o, fmt.Errorf("%s: %w", a, err)
			}
		case "--dry-run":
			o.dryRun = true
		case "--tree":
			o.tree = true
		case "-v", "--verbose":
			o.verbose = true
		case "--allow-risky":
			o.allowRisky = true
		default:
			if o.goal != "" {
				return o, fmt.Errorf("one goal only; quote it (extra %q)", a)
			}
			o.goal = a
		}
	}
	if o.goal == "" {
		return o, fmt.Errorf("usage: iph flow \"GOAL\" [--value NAME=TEXT] [--secret NAME=ENVVAR] [--until TEXT] [--steps N] [--min-conf P] [--dry-run]")
	}
	return o, nil
}

type jevEl struct {
	idx  string
	el   *types.ScreenElement
	kind string // TAP | TOGGLE | TYPE_TEXT
}

// table indexes the visible actionable elements once per observation.
func table(els []types.ScreenElement, h int) ([]jevEl, []string) {
	var out []jevEl
	var context []string
	walk(els, nil, func(e *types.ScreenElement, anc []*types.ScreenElement) {
		label := elLabel(e)
		r := e.Rect
		onScreen := r.Height > 0 && r.Y+r.Height > 0 && r.Y < h
		kind := ""
		switch {
		case e.Type == "Switch" && label != "" && !isRefLabel(label):
			kind = "TOGGLE"
		case fieldTypes[e.Type]:
			kind = "TYPE_TEXT"
		case tapTypes[e.Type] && label != "":
			kind = "TAP"
		}
		if e.Enabled != nil && !*e.Enabled {
			kind = ""
		}
		if kind == "" || !onScreen {
			if e.Type == "StaticText" && label != "" && onScreen && len(context) < 60 {
				context = append(context, label)
			}
			return
		}
		x, y := center(e)
		if kind == "TOGGLE" {
			x = r.X + r.Width - 30
		}
		if coveredBy(els, e, x, y) != nil {
			return
		}
		out = append(out, jevEl{strconv.Itoa(len(out) + 1), e, kind})
	})
	return out, context
}

// isRefLabel drops the inner "0"/"1" value node iOS nests inside a Switch.
func isRefLabel(s string) bool { return s == "0" || s == "1" }

func elLabel(e *types.ScreenElement) string {
	for _, p := range []*string{e.Label, e.Text, e.Name, e.Placeholder, e.Identifier} {
		if s := strings.TrimSpace(deref(p)); s != "" {
			return s
		}
	}
	return ""
}

func describe(j jevEl) map[string]any {
	d := map[string]any{"element": fmt.Sprintf("[%s] %s %q", j.idx, j.el.Type, elLabel(j.el))}
	if v := deref(j.el.Value); v != "" && j.kind != "TOGGLE" {
		d["current_value"] = v
	}
	if j.kind == "TOGGLE" {
		d["state"] = onOff(j.el)
	}
	if j.el.Selected != nil && *j.el.Selected {
		d["selected"] = true
	}
	if j.el.Focused != nil && *j.el.Focused {
		d["focused"] = true
	}
	return d
}

const jevRules = "You operate an iPhone app toward the goal. Choose the single next operation " +
	"that makes progress. Prefer what is visible over scrolling. Choose DONE only when the screen " +
	"shows every requirement of the goal already satisfied. Choose BLOCKED when an error, " +
	"permission prompt, purchase, deletion or sign-out stands in the way, or nothing can progress. " +
	"Never repeat an action that already happened in recent_actions unless the screen changed because of it."

type jevAnswer struct {
	Choice        string             `json:"choice"`
	Confidence    float64            `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities"`
}

func validChoice(a jevAnswer, ids map[string]bool) error {
	if !ids[a.Choice] || len(a.Probabilities) != len(ids) {
		return fmt.Errorf("invalid Jev answer %q", a.Choice)
	}
	sum := 0.0
	for k, p := range a.Probabilities {
		if !ids[k] || math.IsNaN(p) || p < 0 || p > 1 {
			return fmt.Errorf("invalid Jev probabilities")
		}
		sum += p
	}
	if math.Abs(sum-1) > 0.02 {
		return fmt.Errorf("Jev probabilities sum to %.2f", sum)
	}
	return nil
}

func jevKey() (string, error) {
	if k := os.Getenv("TYPESAFE_API_KEY"); k != "" {
		return k, nil
	}
	return "", fmt.Errorf("no Jev key: set TYPESAFE_API_KEY (https://typesafe.ai)")
}

func jevAsk(key string, body map[string]any) (map[string]jevAnswer, error) {
	buf, _ := json.Marshal(body)
	var resp *http.Response
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		req, _ := http.NewRequest("POST", jevURL, bytes.NewReader(buf))
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		resp, err = jevHTTP.Do(req)
		if err == nil && (resp.StatusCode == 429 || resp.StatusCode == 503 || resp.StatusCode == 529) && attempt < 2 {
			resp.Body.Close()
			time.Sleep(time.Duration(500<<attempt) * time.Millisecond)
			continue
		}
		break
	}
	if err != nil {
		return nil, fmt.Errorf("Jev request failed; no action executed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("Jev returned HTTP %d; no action executed", resp.StatusCode)
	}
	var out struct {
		Answers map[string]jevAnswer `json:"answers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("Jev response unreadable; no action executed")
	}
	return out.Answers, nil
}

type doneStep struct {
	Action      string `json:"action"`
	PageChanged bool   `json:"page_changed"`
}

// settled returns a screen that has stopped moving. Stream reads cost ~50 ms,
// so it waits for three consecutive identical frames (~100 ms of stillness;
// two matched mid-scroll when a list coasted slowly) that differ from the
// screen acted on, or ~0.6 s of no change at all (the action did nothing
// visible). Tree reads cost ~0.85 s, so it only re-reads while the screen
// still equals the one acted on.
func settled(read func() ([]types.ScreenElement, string, error), before string, cheap bool) ([]types.ScreenElement, string, error) {
	els, text, err := read()
	if err != nil || before == "" && !cheap {
		return els, text, err
	}
	if !cheap {
		for i := 0; i < 2 && text == before; i++ {
			if els, text, err = read(); err != nil {
				return els, text, err
			}
		}
		return els, text, nil
	}
	// OCR reads one page slightly differently frame to frame ("(Ii)) Cellular"
	// vs "i (Iil) Cellular"), so pages are compared by how many lines they
	// share, not byte for byte: >= 0.8 is the same page.
	changed := similar(text, before) < 0.8
	same := 0
	for i := 0; i < 40; i++ {
		n, t, e := read()
		if e != nil {
			return n, t, e
		}
		if similar(t, text) >= 0.95 {
			same++
		} else {
			same = 0
		}
		els, text = n, t
		if !changed && similar(t, before) < 0.8 {
			changed, same = true, 0
		}
		if same >= 2 && (changed || i >= 14) {
			break
		}
	}
	return els, text, nil
}

// similar is the share of lines two screen readings have in common (Jaccard).
func similar(a, b string) float64 {
	set := func(s string) map[string]bool {
		m := map[string]bool{}
		for _, l := range strings.Split(s, "\n") {
			if l = strings.TrimSpace(l); l != "" {
				m[l] = true
			}
		}
		return m
	}
	x, y := set(a), set(b)
	if len(x) == 0 && len(y) == 0 {
		return 1
	}
	inter := 0
	for l := range x {
		if y[l] {
			inter++
		}
	}
	return float64(inter) / float64(len(x)+len(y)-inter)
}

func dumpText(c *devicekit.DeviceKitClient) ([]types.ScreenElement, string, error) {
	els, err := dump(c)
	if err != nil {
		return nil, "", err
	}
	return els, types.FormatText(els), nil
}

// risky labels end a run unless the caller opted in: Jev must not be the one
// to buy, delete, erase or sign out.
var riskyWords = []string{"delete", "remove", "erase", "reset", "sign out", "log out", "logout",
	"buy", "purchase", "subscribe", "pay ", "pay\u00a0", "confirm payment", "uninstall", "factory",
	"allow paste", // an app asking to read the clipboard
	// Chinese shopping and account apps (Taobao, JD, HKTVmall)
	"购买", "購買", "下单", "下單", "提交订单", "提交訂單", "支付", "付款", "结算", "結算", "删除", "刪除",
	"退出登录", "退出登錄", "登出", "订阅", "訂閱", "确认收货", "確認收貨"}

func risky(label string) bool {
	l := strings.ToLower(label) + " "
	for _, w := range riskyWords {
		if strings.Contains(l, w) {
			return true
		}
	}
	return false
}

// named reports the goal mentions this label (case- and punctuation-blind,
// at least four letters, so "On" or "OK" never match by accident).
func named(goal, label string) bool {
	l := strings.ToLower(norm(label))
	return len([]rune(l)) >= 4 && strings.Contains(strings.ToLower(norm(goal)), l)
}

// tapped reports the label was already tapped in this run.
func tapped(h []doneStep, label string) bool {
	for _, s := range h {
		if s.Action == fmt.Sprintf("tapped %q", label) {
			return true
		}
	}
	return false
}

// looping reports the last n actions all left the screen unchanged.
func looping(h []doneStep, n int) bool {
	if len(h) < n {
		return false
	}
	for _, s := range h[len(h)-n:] {
		if s.PageChanged {
			return false
		}
	}
	return true
}

func doGoal(c *devicekit.DeviceKitClient, args []string) error {
	o, err := parseDo(args)
	if err != nil {
		return err
	}
	// The key lookup starts now and is awaited at the first Jev call, so a slow
	// lookup overlaps the deep link, the stream connect and the first read.
	getKey := sync.OnceValues(jevKey)
	go getKey()
	// Open the TLS connection to Jev now, so step 1 does not pay the handshake.
	go func() {
		if r, err := jevHTTP.Head("https://api.typesafe.ai/"); err == nil {
			r.Body.Close()
		}
	}()
	// A goal that names a Settings page opens it by deep link (~0.4 s);
	// Jev then only checks or finishes.
	// Only for goals about Settings: the goal says so, or Settings is already
	// in front. "Open Notifications in Slack" must not jump to iOS Settings.
	// replay is the same run as `iph batch` steps: a flow done once can be rerun
	// with no model. Typing is left out (values are named, never recorded).
	done, tried := runSavedReplay(c, o)
	if done {
		return nil
	}
	var replay []string
	replayable := true
	// saved only when every tap is found by label, and only from a clean start: after a
	// failed replay this run records just the rest of the task
	save := !tried
	finish := func() {
		printReplay(replay, replayable)
		if replayable && save && len(replay) > 0 {
			storeReplay(o, replay)
		}
	}
	if title, link := deepLinkFor(o.goal); link != "" && aboutSettings(c, o.goal) {
		// A prefs: link opens Settings on that page from whatever app is in front.
		t0 := time.Now()
		if err := openURL(c, link); err == nil {
			fmt.Printf(" 0 link %4dms %s -> %s\n", time.Since(t0).Milliseconds(), title, link)
			replay = append(replay, "url:"+link, "wait:"+title+":5")
			// If the goal ends on that page, the page's own title is the
			// proof: check the navigation bar, no model call needed.
			if goalEndsOn(o.goal, title) && navTitleIs(c, title, 2*time.Second) {
				fmt.Printf("done: deep link in %.1fs (navigation bar reads %q)\n", time.Since(t0).Seconds(), title)
				finish()
				return nil
			}
		}
	}
	ws, err := c.GetWindowSize()
	if err != nil {
		return err
	}
	w, h := ws.ScreenSize.Width, ws.ScreenSize.Height
	var ocr *ocrProc
	var fr *frames
	if !o.tree {
		if ocr, err = startOCR(); err == nil {
			fr, err = startFrames(agentAddr)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "iph flow: "+err.Error()+"; using the slower accessibility tree")
			if ocr != nil {
				ocr.close()
			}
			ocr = nil
		} else {
			defer ocr.close()
			defer fr.stop()
		}
	}
	// stream frames are streamScale% of the screen's pixels
	pxPerPoint := float64(ws.Scale) * streamScale / 100
	// perceive reads the screen: OCR when available, the tree when asked or
	// when the last decision needs controls OCR cannot see.
	perceive := func(tree bool) ([]types.ScreenElement, string, error) {
		if tree || ocr == nil {
			return dumpText(c)
		}
		img, err := fr.next()
		if err != nil {
			return nil, "", err
		}
		lines, err := ocr.read(img, pxPerPoint)
		if err != nil {
			return nil, "", err
		}
		var b strings.Builder
		for _, l := range lines {
			b.WriteString(l.T + "\n")
		}
		return ocrElements(lines), b.String(), nil
	}
	var history []doneStep
	start := time.Now()
	lastScreen := ""
	useTree := o.tree || len(o.values) > 0 // fields and their values live in the tree
	var pending chan error                 // a tap still in flight from the previous step
	for i := 1; i <= o.steps; i++ {
		tRead := time.Now()
		els, screen, err := settled(func() ([]types.ScreenElement, string, error) { return perceive(useTree) }, lastScreen, !useTree && ocr != nil)
		if err != nil {
			return err
		}
		if pending != nil {
			if err := <-pending; err != nil {
				return err
			}
			pending = nil
		}
		readMs := time.Since(tRead).Milliseconds()
		mode := map[bool]string{true: "tree", false: "ocr"}[useTree || ocr == nil]
		if len(history) > 0 {
			history[len(history)-1].PageChanged = screen != lastScreen
		}
		lastScreen = screen
		if untilSeen(o.until, screen) {
			fmt.Printf("done: %q on screen after %d action(s) in %.1fs\n", o.until, i-1, time.Since(start).Seconds())
			finish()
			remember(els)
			return nil
		}
		// The tree is read while Jev decides, so an OCR tap can be saved by its
		// label (OCR text is not a tree label) at no extra wait.
		var treeCh chan []types.ScreenElement
		if mode == "ocr" {
			treeCh = make(chan []types.ScreenElement, 1)
			go func() { t, _ := dump(c); treeCh <- t }()
		}
		items, context := table(els, h)
		// "Search" is a trap for navigation goals: tapping it swaps the list
		// for a results view where the target sits under other sections (every
		// Settings flow opened with it, 2026-10-10). Offer it only when the
		// goal itself asks to search. Describing it to Jev instead was tried
		// and failed: OCR reads it as a plain button, and Jev still tapped it
		// first in 6/6 runs (Camera 0/2).
		if !strings.Contains(strings.ToLower(o.goal), "search") {
			kept := items[:0]
			for _, it := range items {
				if l := strings.ToLower(elLabel(it.el)); l == "search" || it.el.Type == "SearchField" {
					continue
				}
				kept = append(kept, it)
			}
			items = kept
			for n := range items {
				items[n].idx = strconv.Itoa(n + 1)
			}
		}

		ops := map[string]any{
			"SCROLL_DOWN": "Scroll down: no listed item advances the goal, and what is needed is likely further down.",
			"SCROLL_UP":   "Scroll up: no listed item advances the goal, and what is needed is likely further up.",
			"DONE":        "Every requirement of the goal is visibly satisfied.",
			"BLOCKED":     "Nothing supported can progress, or a risky/unknown prompt needs a person.",
		}
		heads := map[string]map[string]any{}
		ids := map[string]map[string]bool{}
		byKind := map[string]map[string]jevEl{}
		for _, it := range items {
			if heads[it.kind] == nil {
				heads[it.kind], ids[it.kind], byKind[it.kind] = map[string]any{}, map[string]bool{}, map[string]jevEl{}
			}
			heads[it.kind][it.idx] = describe(it)
			ids[it.kind][it.idx] = true
			byKind[it.kind][it.idx] = it
		}
		if heads["TAP"] != nil {
			ops["TAP"] = "Tap one of the listed on-screen items: one of them is the next step toward the goal."
		}
		if heads["TOGGLE"] != nil {
			ops["TOGGLE"] = "Flip one of the listed switches: the goal needs it in the other state."
		}
		if heads["TYPE_TEXT"] != nil && len(o.values) > 0 {
			ops["TYPE_TEXT"] = "Replace the text in an input field with one of the supplied values."
		}
		opIDs := map[string]bool{}
		for k := range ops {
			opIDs[k] = true
		}
		var elements []map[string]any
		for _, it := range items {
			d := describe(it)
			d["operation"] = it.kind
			elements = append(elements, d)
		}
		var visible []string // the op head must know what is on screen to rule out scrolling
		for _, it := range items {
			visible = append(visible, fmt.Sprintf("%s %q", strings.ToLower(it.kind), elLabel(it.el)))
		}
		questions := map[string]any{
			"operation": map[string]any{"type": "choice", "criteria": ops,
				"instructions": map[string]any{"goal": o.goal, "rules": jevRules, "on_screen_items": visible}},
		}
		for kind, crit := range heads {
			if kind == "TYPE_TEXT" && len(o.values) == 0 {
				continue
			}
			questions[strings.ToLower(kind)+"_target"] = map[string]any{"type": "choice", "criteria": crit,
				"instructions": map[string]any{"goal": o.goal, "operation": kind, "rules": jevRules}}
		}
		valIDs := map[string]bool{}
		if _, ok := ops["TYPE_TEXT"]; ok {
			crit := map[string]any{}
			for n, v := range o.values {
				id := strconv.Itoa(n + 1)
				crit[id] = "value named " + v.name // the value itself never leaves the Mac
				valIDs[id] = true
			}
			questions["type_text_value"] = map[string]any{"type": "choice", "criteria": crit,
				"instructions": map[string]any{"goal": o.goal, "rule": "Pick the supplied value that belongs in the chosen field."}}
		}
		recent := history
		if len(recent) > 8 {
			recent = recent[len(recent)-8:]
		}
		body := map[string]any{
			"model":     env("TYPESAFE_MODEL", "jev-latest"),
			"state":     map[string]any{"screen_text": context, "elements": elements, "recent_actions": recent},
			"questions": questions,
		}
		t0 := time.Now()
		key, err := getKey()
		if err != nil {
			return err
		}
		ans, err := jevAsk(key, body)
		if err != nil {
			return err
		}
		lat := time.Since(t0).Milliseconds()
		op := ans["operation"]
		if err := validChoice(op, opIDs); err != nil {
			return err
		}
		// The operation head wavers on long lists (it scrolled past a visible
		// "Keyboard" eight times, 2026-10-10) while the tap head is sure. When
		// the tap head picks, with high confidence, an item the goal names, a
		// scroll or a low-confidence op is overruled: tap it.
		if (op.Choice == "SCROLL_DOWN" || op.Choice == "SCROLL_UP" || op.Confidence < 0.5) && op.Choice != "DONE" {
			if ta, ok := ans["tap_target"]; ok && validChoice(ta, ids["TAP"]) == nil && ta.Confidence >= 0.85 {
				if named(o.goal, elLabel(byKind["TAP"][ta.Choice].el)) && !tapped(history, elLabel(byKind["TAP"][ta.Choice].el)) {
					op = jevAnswer{Choice: "TAP", Confidence: ta.Confidence, Probabilities: op.Probabilities}
				}
			}
		}
		line := fmt.Sprintf("%2d %-4s read %4dms jev %4dms %-11s p=%.2f", i, mode, readMs, lat, op.Choice, op.Confidence)
		// OCR sees text, not switches or empty fields. Before giving up on an
		// OCR screen, decide once more from the accessibility tree.
		retryTree := func() bool {
			if mode == "ocr" {
				useTree, lastScreen = true, ""
				fmt.Println(line + "  -> rereading with the accessibility tree")
				return true
			}
			return false
		}
		stop := func(msg string) error {
			fmt.Println(line + "  " + msg)
			for _, it := range items { // the choices Jev saw; 'iph dump' for the full tree
				fmt.Printf("   %s [%s] %s %q\n", it.el.Ref, it.kind, it.el.Type, elLabel(it.el))
			}
			remember(els)
			return fmt.Errorf("%s after %d step(s), %.1fs", msg, i-1, time.Since(start).Seconds())
		}
		// Jev decides; the caller is asked only when acting would be unsafe.
		// A plurality pick on a navigation step is cheap to undo (the next
		// cycle sees the result), so it proceeds. It stops only when the pick
		// is a near coin-flip, is risky by its own label, or repeats a
		// sequence that already failed to change the screen.
		floor := o.minConf
		// all reversible: a tap, a scroll, or typing a named value into a field
		if op.Choice == "SCROLL_DOWN" || op.Choice == "SCROLL_UP" || op.Choice == "TAP" || op.Choice == "TYPE_TEXT" {
			floor = min(o.minConf, 0.3)
		}
		if op.Confidence < floor {
			if retryTree() {
				i--
				continue
			}
			return stop(fmt.Sprintf("low confidence (%.2f < %.2f); choices above", op.Confidence, floor))
		}
		var target jevEl
		if kind := op.Choice; kind == "TAP" || kind == "TOGGLE" || kind == "TYPE_TEXT" {
			ta := ans[strings.ToLower(kind)+"_target"]
			if err := validChoice(ta, ids[kind]); err != nil {
				return err
			}
			tfloor := o.minConf
			if kind == "TAP" {
				tfloor = min(o.minConf, 0.4)
			}
			if ta.Confidence < tfloor {
				if retryTree() {
					i--
					continue
				}
				return stop(fmt.Sprintf("low target confidence (%.2f) for %s; choices above", ta.Confidence, kind))
			}
			target = byKind[kind][ta.Choice]
			if g := strings.ToLower(o.goal); kind == "TYPE_TEXT" && elLabel(target.el) == "Address" && !strings.Contains(g, "address") && !strings.Contains(g, "url") {
				return stop("the page has no search field of its own, only Safari's address bar; open the site's search URL instead (iph url)")
			}
			line += fmt.Sprintf("  [%s] %s %q p=%.2f", target.idx, target.el.Type, elLabel(target.el), ta.Confidence)
			if risky(elLabel(target.el)) && !o.allowRisky {
				return stop(fmt.Sprintf("%q looks irreversible; rerun with --allow-risky if intended", elLabel(target.el)))
			}
		}
		if looping(history, 4) {
			return stop("the last actions are not changing the screen; choices above")
		}
		switch op.Choice {
		case "DONE":
			fmt.Println(line)
			if o.until != "" { // Jev's DONE is a claim; --until is the proof
				return stop(fmt.Sprintf("Jev chose DONE but %q is not on screen", o.until))
			}
			fmt.Printf("done: %d action(s) in %.1fs (verify the outcome independently)\n", i-1, time.Since(start).Seconds())
			finish()
			remember(els)
			return nil
		case "BLOCKED":
			if retryTree() {
				i--
				continue
			}
			return stop("blocked; choices above")
		}
		fmt.Println(line)
		if o.verbose {
			var ls []string
			for _, it := range items {
				ls = append(ls, elLabel(it.el))
			}
			fmt.Printf("           saw %q\n", ls)
		}
		if o.dryRun {
			fmt.Println("dry run: nothing executed")
			return nil
		}
		var tree []types.ScreenElement
		if treeCh != nil {
			tree = <-treeCh // the device is not shared with a dump in flight
		}
		act := op.Choice
		switch op.Choice {
		case "TAP":
			// Fire the tap and read the screen while the agent is still
			// returning (~0.33 s of RPC). The stream already shows the result;
			// settled() waits for it to stop moving, so nothing is lost.
			x, y := center(target.el)
			tapErr := make(chan error, 1)
			go func() { tapErr <- c.Gesture(taps(x, y)) }()
			if fr == nil { // tree mode reads only after the tap has landed
				if err := <-tapErr; err != nil {
					return err
				}
			} else {
				pending = tapErr
			}
			act = fmt.Sprintf("tapped %q", elLabel(target.el))
			lab := elLabel(target.el)
			if mode == "ocr" { // OCR text is not a tree label ("AA Display & Text Size")
				lab = labelAt(tree, x, y)
			}
			if lab != "" {
				replay = append(replay, "wait:"+lab+":5", "tap:"+lab)
			} else {
				replay = append(replay, "sleep:1200", fmt.Sprintf("tap:%d,%d", x, y))
				save = false
			}
		case "TOGGLE":
			r := target.el.Rect
			if err := c.Tap(r.X+r.Width-30, r.Y+r.Height/2); err != nil {
				return err
			}
			act = fmt.Sprintf("toggled %q (was %s)", elLabel(target.el), onOff(target.el))
			replay = append(replay, "wait:"+elLabel(target.el)+":5", "togg:"+elLabel(target.el))
		case "TYPE_TEXT":
			va := ans["type_text_value"]
			if err := validChoice(va, valIDs); err != nil {
				return err
			}
			n, _ := strconv.Atoi(va.Choice)
			v := o.values[n-1]
			if err := typeInto(c, els, target.el, v.text); err != nil {
				return err
			}
			act = fmt.Sprintf("typed value %q into %q", v.name, elLabel(target.el))
			replayable = false
			fmt.Printf("           value=%s\n", v.name)
		case "SCROLL_DOWN", "SCROLL_UP":
			d := h * 4 / 10
			if op.Choice == "SCROLL_UP" {
				d = -d
			}
			if err := c.Swipe(w/2, h/2+d/2, w/2, h/2-d/2, 900); err != nil {
				return err
			}
			dir := "up" // batch swipes name the finger's direction: scrolling down moves it up
			if op.Choice == "SCROLL_UP" {
				dir = "down"
			}
			replay = append(replay, "swipe:"+dir+":900", "sleep:900")
			// The RPC returns at once while the finger is still moving; frames
			// read during the drag are wasted OCR. The list stops with it (no
			// momentum at this speed).
			time.Sleep(900 * time.Millisecond)
		}
		history = append(history, doneStep{Action: act})
		useTree = o.tree || len(o.values) > 0
	}
	return fmt.Errorf("step budget (%d) used up without DONE after %.1fs; run 'iph dump'", o.steps, time.Since(start).Seconds())
}

// printReplay prints the run as one `iph batch` command, or says why it cannot.
func printReplay(steps []string, ok bool) {
	if !ok {
		fmt.Println("replay: not printed, the flow typed a value; use iph fill for forms")
		return
	}
	if len(steps) == 0 {
		return
	}
	q := make([]string, len(steps))
	for i, s := range steps {
		q[i] = "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
	}
	fmt.Println("replay: iph batch " + strings.Join(q, " "))
}

// untilSeen reports whether the --until text is on the screen just read.
func untilSeen(until, screen string) bool {
	return until != "" && strings.Contains(strings.ToLower(screen), strings.ToLower(until))
}
