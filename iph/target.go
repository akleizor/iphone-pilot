package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/mobile-next/mobilecli/devices/devicekit"
	"github.com/mobile-next/mobilecli/types"
)

// A target is "x,y", "@eN" (from the latest dump) or a label: exact
// label/text/name/identifier, falling back to a unique case-insensitive substring.
//
// Refs are positional, so iph remembers what each ref pointed at in the last
// dump and refuses (or follows the element) when a fresh dump disagrees.

var refsFile = filepath.Join(devDir, "last-dump.json")

// errAmbiguous marks a label that names several elements (callers treat it as present).
var errAmbiguous = errors.New("ambiguous label")

// sig identifies an element across dumps independent of its position.
type sig struct {
	Type, Label, ID string
}

func sigOf(e *types.ScreenElement) sig {
	return sig{e.Type, deref(e.Label), deref(e.Identifier)}
}

func (s sig) String() string {
	return fmt.Sprintf("[%s] %q", s.Type, s.Label)
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func dump(c *devicekit.DeviceKitClient) ([]types.ScreenElement, error) {
	els, err := c.GetSourceElements()
	if err != nil {
		return nil, err
	}
	// An app asking to read the clipboard (JD on every launch) blocks the screen
	// behind a system alert: refuse it, which keeps the clipboard private, and read again.
	if deny := pasteDeny(els); deny != nil {
		x, y := center(deny)
		if c.Tap(x, y) == nil {
			time.Sleep(300 * time.Millisecond)
			if els, err = c.GetSourceElements(); err != nil {
				return nil, err
			}
		}
	}
	types.AttachRefs(els)
	annotate(els)
	return els, nil
}

// pasteDeny is the refuse button of a "would like to paste" alert, or nil.
func pasteDeny(els []types.ScreenElement) *types.ScreenElement {
	var alert bool
	var deny *types.ScreenElement
	walk(els, nil, func(e *types.ScreenElement, _ []*types.ScreenElement) {
		l := deref(e.Label)
		if strings.Contains(l, "would like to paste") || strings.Contains(l, "粘贴") || strings.Contains(l, "貼上") {
			alert = true
		}
		if e.Type == "Button" && (strings.HasPrefix(l, "Don’t Allow Paste") || strings.HasPrefix(l, "Don't Allow Paste") || l == "不允许粘贴" || l == "不允許貼上") {
			deny = e
		}
	})
	if !alert {
		return nil
	}
	return deny
}

// dumpAndRemember is the user-facing dump: it records ref -> sig for later checks.
func dumpAndRemember(c *devicekit.DeviceKitClient) ([]types.ScreenElement, error) {
	els, err := dump(c)
	if err != nil {
		return nil, err
	}
	remember(els)
	return els, nil
}

func remember(els []types.ScreenElement) {
	refs := map[string]sig{}
	walk(els, nil, func(e *types.ScreenElement, _ []*types.ScreenElement) { refs[e.Ref] = sigOf(e) })
	if b, err := json.Marshal(refs); err == nil {
		_ = os.MkdirAll(filepath.Dir(refsFile), 0o755)
		_ = os.WriteFile(refsFile, b, 0o644)
	}
}

// annotate surfaces state FormatText would otherwise hide: switch value "1"
// as "(checked)", slider/progress values, and a text field's typed content.
func annotate(els []types.ScreenElement) {
	on := true
	for i := range els {
		e := &els[i]
		switch e.Type {
		case "Switch":
			if deref(e.Value) == "1" {
				e.Checked = &on
			}
		case "Slider", "ProgressIndicator", "Stepper", "PageIndicator":
			if v := deref(e.Value); v != "" {
				t := strings.TrimSpace(deref(e.Label) + ": " + v)
				e.Text = &t
			}
		case "TextField", "SearchField", "SecureTextField", "TextView":
			if v := deref(e.Value); v != "" && v != deref(e.Placeholder) && v != deref(e.Label) {
				e.Text = &v
			}
		}
		annotate(e.Children)
	}
}

// walk visits elements depth-first pre-order (dump order) with their ancestors.
func walk(els []types.ScreenElement, anc []*types.ScreenElement, f func(*types.ScreenElement, []*types.ScreenElement)) {
	for i := range els {
		f(&els[i], anc)
		walk(els[i].Children, append(anc[:len(anc):len(anc)], &els[i]), f)
	}
}

// find returns the first element (dump order) satisfying pred, or nil.
func find(els []types.ScreenElement, pred func(*types.ScreenElement) bool) *types.ScreenElement {
	for i := range els {
		if pred(&els[i]) {
			return &els[i]
		}
		if f := find(els[i].Children, pred); f != nil {
			return f
		}
	}
	return nil
}

func findRef(els []types.ScreenElement, ref string) *types.ScreenElement {
	return find(els, func(e *types.ScreenElement) bool { return e.Ref == ref })
}

func coords(target string) (x, y int, ok bool) {
	n, _ := fmt.Sscanf(target, "%d,%d", &x, &y)
	return x, y, n == 2 && !strings.HasPrefix(target, "@")
}

// resolve returns the element a target names on a fresh dump (nil for x,y).
func resolve(c *devicekit.DeviceKitClient, target string) (*types.ScreenElement, []types.ScreenElement, int, int, error) {
	if x, y, ok := coords(target); ok {
		return nil, nil, x, y, nil
	}
	els, err := dump(c)
	if err != nil {
		return nil, nil, 0, 0, err
	}
	el, x, y, err := resolveIn(els, target)
	return el, els, x, y, err
}

// resolveIn resolves a target against an existing dump (nil element for x,y).
func resolveIn(els []types.ScreenElement, target string) (*types.ScreenElement, int, int, error) {
	if x, y, ok := coords(target); ok {
		return nil, x, y, nil
	}
	var el *types.ScreenElement
	var err error
	if strings.HasPrefix(target, "@") {
		el, err = resolveRef(els, target)
	} else {
		el, err = resolveLabel(els, target)
	}
	if err != nil {
		return nil, 0, 0, err
	}
	x, y := center(el)
	return el, x, y, nil
}

func resolveRef(els []types.ScreenElement, ref string) (*types.ScreenElement, error) {
	el := findRef(els, ref)
	want, known := lastSig(ref)
	if !known {
		if el == nil {
			return nil, fmt.Errorf("ref %s not on current screen; run 'iph dump'", ref)
		}
		return el, nil
	}
	if el != nil && sigOf(el) == want {
		return el, nil
	}
	// The screen changed since the dump that issued the ref: follow the element
	// if it is still uniquely present, otherwise refuse rather than tap a stranger.
	var matches []*types.ScreenElement
	walk(els, nil, func(e *types.ScreenElement, _ []*types.ScreenElement) {
		if sigOf(e) == want {
			matches = append(matches, e)
		}
	})
	if len(matches) == 1 {
		fmt.Fprintf(os.Stderr, "iph: %s moved to %s %s\n", ref, matches[0].Ref, want)
		return matches[0], nil
	}
	now := "nothing"
	if el != nil {
		now = sigOf(el).String()
	}
	return nil, fmt.Errorf("screen changed: %s was %s, now %s; run 'iph dump' again", ref, want, now)
}

func lastSig(ref string) (sig, bool) {
	b, err := os.ReadFile(refsFile)
	if err != nil {
		return sig{}, false
	}
	var refs map[string]sig
	if json.Unmarshal(b, &refs) != nil {
		return sig{}, false
	}
	s, ok := refs[ref]
	return s, ok
}

func resolveLabel(els []types.ScreenElement, label string) (*types.ScreenElement, error) {
	names := func(e *types.ScreenElement) []string {
		return []string{deref(e.Label), deref(e.Text), deref(e.Name), deref(e.Identifier), deref(e.Placeholder)} // dump shows a field by its placeholder
	}
	collect := func(match func(string) bool) []*types.ScreenElement {
		var out []*types.ScreenElement
		walk(els, nil, func(e *types.ScreenElement, anc []*types.ScreenElement) {
			for _, a := range anc {
				if len(out) > 0 && a == out[len(out)-1] {
					return // nested inside the previous match (e.g. a cell's own label)
				}
			}
			for _, n := range names(e) {
				if n != "" && match(n) {
					out = append(out, e)
					return
				}
			}
		})
		return out
	}
	label = norm(label)
	m := collect(func(n string) bool { return norm(n) == label })
	if len(m) == 0 {
		lower := strings.ToLower(label)
		m = collect(func(n string) bool { return strings.Contains(strings.ToLower(norm(n)), lower) })
	}
	switch len(m) {
	case 0:
		return nil, fmt.Errorf("no element labelled %q on screen; check 'iph dump' (it may need a scroll)", label)
	case 1:
		return m[0], nil
	}
	var list []string
	for _, e := range m {
		list = append(list, fmt.Sprintf("%s %s", e.Ref, sigOf(e)))
	}
	return nil, fmt.Errorf("%w: %q matches %d elements, use a ref: %s", errAmbiguous, label, len(m), strings.Join(list, ", "))
}

// norm drops invisible format characters (bidi marks like "\u2068Rp\u2069",
// zero-width spaces) and folds every Unicode space ("Face\u00a0ID") to one
// plain space, so a label typed as it reads still matches.
func norm(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
	return strings.Join(strings.FieldsFunc(s, unicode.IsSpace), " ")
}

func center(e *types.ScreenElement) (int, int) {
	return e.Rect.X + e.Rect.Width/2, e.Rect.Y + e.Rect.Height/2
}

// overlayTypes are bars drawn above content that swallow touches meant for rows
// beneath. Apps also use these types as full-screen containers (Settings wraps
// the screen in a Toolbar), so only bar-sized ones (< 40% of the screen) count.
var overlayTypes = map[string]bool{
	"SearchField": true, "TabBar": true, "Toolbar": true, "NavigationBar": true, "Keyboard": true,
}

// screenHeight is the visible screen, not the content extent. A scrolled list
// reports rows thousands of points down; measuring against that made a
// full-screen Toolbar (some apps wrap every screen in one) look bar-sized, so
// every control was "covered" (2026-10-09). The screen is the tallest
// container anchored at the top; the content extent is only a fallback.
func screenHeight(els []types.ScreenElement) int {
	top, extent := 0, 0
	walk(els, nil, func(e *types.ScreenElement, _ []*types.ScreenElement) {
		if e.Rect.Y <= 0 && e.Rect.X <= 0 {
			top = max(top, e.Rect.Height)
		}
		extent = max(extent, e.Rect.Y+e.Rect.Height)
	})
	if top > 0 {
		return top
	}
	return extent
}

// coveredBy reports an overlay that is not the target, its ancestor or its
// descendant, yet contains the tap point — the tap would land on the overlay.
func coveredBy(els []types.ScreenElement, target *types.ScreenElement, x, y int) *types.ScreenElement {
	screenH := screenHeight(els)
	related := map[*types.ScreenElement]bool{target: true} // target and its ancestors
	walk(els, nil, func(e *types.ScreenElement, anc []*types.ScreenElement) {
		if e == target {
			for _, a := range anc {
				related[a] = true
			}
		}
	})
	var hit *types.ScreenElement
	walk(els, nil, func(e *types.ScreenElement, anc []*types.ScreenElement) {
		if hit != nil || related[e] || !overlayTypes[e.Type] || e.Rect.Height*10 >= screenH*4 {
			return
		}
		for _, a := range anc {
			if a == target {
				return // a descendant of the target is part of it
			}
		}
		if rectHas(e.Rect, x, y) {
			hit = e
		}
	})
	return hit
}

// uncovered refuses a touch point that lands on an overlay instead of el.
func uncovered(els []types.ScreenElement, el *types.ScreenElement, x, y int) error {
	if o := coveredBy(els, el, x, y); o != nil {
		return fmt.Errorf("%s %s is under %s %s at (%d,%d); scroll it clear first", el.Ref, sigOf(el), o.Ref, sigOf(o), x, y)
	}
	return nil
}

// point resolves a target for a touch and refuses points hidden under an overlay.
func point(c *devicekit.DeviceKitClient, target string) (int, int, error) {
	el, els, x, y, err := resolve(c, target)
	if err != nil || el == nil {
		return x, y, err
	}
	err = uncovered(els, el, x, y)
	if o := coveredBy(els, el, x, y); o != nil && o.Type == "Keyboard" {
		// A keyboard is not a choice the caller made: hide it and re-resolve,
		// the way a person dismisses it to reach the button underneath.
		dismissKeyboard(c)
		el, els, x, y, err = resolve(c, target)
		if err != nil || el == nil {
			return x, y, err
		}
		return x, y, uncovered(els, el, x, y)
	}
	return x, y, err
}

// dismissKeyboard hides the on-screen keyboard (Return), for a target or a
// submit button it covers. One place, so point() and fill agree.
func dismissKeyboard(c *devicekit.DeviceKitClient) {
	if keys, err := combos([]string{"return"}); err == nil {
		_ = c.PressKeys(keys)
	}
	time.Sleep(300 * time.Millisecond)
}

func rectHas(r types.ScreenElementRect, x, y int) bool {
	return x >= r.X && x < r.X+r.Width && y >= r.Y && y < r.Y+r.Height
}
