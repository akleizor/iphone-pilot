package main

// `iph fill`: a whole form in one call, with no model in the loop.
//
//	iph fill "Username=jane" "Password=$PW_ENV" ... [--submit LABEL]
//
// Each FIELD is matched against the visible text fields by label, placeholder,
// identifier or current value (exact, then unique substring). The field is
// tapped, focus is PROVEN by one tree read before a single key is typed (a tap
// that leaves the keyboard on the previous field put a password into Username,
// 2026-10-10), any old value is cleared, and the text is typed. A VALUE of
// `$NAME` reads environment variable NAME, so secrets never sit in argv.
// --submit taps the named button once every field is filled.
//
// One tree read per field plus the typing: a 3-field sign-in is ~3 s, against
// minutes when an agent drives it tap by tap.

import (
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mobile-next/mobilecli/devices/devicekit"
	"github.com/mobile-next/mobilecli/types"
)

func fill(c *devicekit.DeviceKitClient, args []string) error {
	var pairs [][2]string
	secret := map[string]bool{}
	submit := ""
	for i := 0; i < len(args); i++ {
		if args[i] == "--submit" && i+1 < len(args) {
			submit = args[i+1]
			i++
			continue
		}
		k, v, ok := strings.Cut(args[i], "=")
		if !ok || k == "" {
			return fmt.Errorf("want FIELD=VALUE, got %q", args[i])
		}
		if env, isEnv := strings.CutPrefix(v, "$"); isEnv {
			if v = os.Getenv(env); v == "" {
				return fmt.Errorf("%s: $%s is empty", k, env)
			}
			secret[k] = true
		}
		pairs = append(pairs, [2]string{k, v})
	}
	if len(pairs) == 0 {
		return fmt.Errorf(`usage: iph fill "FIELD=VALUE"... [--submit LABEL]`)
	}
	start := time.Now()
	// Fast path: the runner fills the whole form in one round trip
	// (device.io.fill: find, tap, prove focus, clear, type, verify, submit,
	// all on device). Older runners lack it; fall back to the step path.
	if done, err := fillOnDevice(c, pairs, secret, submit); done || err != nil {
		if err == nil {
			fmt.Printf("filled %d field(s) in %.2fs (one call)\n", len(pairs), time.Since(start).Seconds())
		}
		return err
	}
	els, err := dump(c)
	if err != nil {
		return err
	}
	for _, p := range pairs {
		f, err := field(els, p[0])
		if err != nil {
			return err
		}
		if err := typeInto(c, f, p[1]); err != nil {
			return fmt.Errorf("%q: %w", p[0], err)
		}
		shown := p[1]
		if f.Type == "SecureTextField" || secret[p[0]] {
			shown = "••••"
		}
		fmt.Printf("%s = %s\n", p[0], shown)
	}
	if submit != "" {
		// The keyboard covers buttons below the fields (Connect sat under it,
		// so the tap typed instead of submitting). Return dismisses it.
		dismissKeyboard(c)
		if els, err = dump(c); err != nil {
			return err
		}
		b, err := resolveLabel(els, submit)
		if err != nil {
			return err
		}
		if b.Enabled != nil && !*b.Enabled {
			return fmt.Errorf("%q is still disabled after filling; a value was not accepted", submit)
		}
		x, y := center(b)
		if err := c.Tap(x, y); err != nil {
			return err
		}
		fmt.Printf("tapped %q\n", submit)
	}
	fmt.Printf("filled %d field(s) in %.1fs\n", len(pairs), time.Since(start).Seconds())
	return nil
}

func fillOnDevice(c *devicekit.DeviceKitClient, pairs [][2]string, secret map[string]bool, submit string) (bool, error) {
	var fields []map[string]string
	for _, p := range pairs {
		fields = append(fields, map[string]string{"match": p[0], "text": p[1]})
	}
	params := map[string]any{"fields": fields}
	if submit != "" {
		params["submit"] = submit
	}
	_, err := c.CallRPC("device.io.fill", params)
	if unsupported(err) || unresolved(err) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	for _, p := range pairs {
		v := p[1]
		if secret[p[0]] {
			v = "••••"
		}
		fmt.Printf("%s = %s\n", p[0], v)
	}
	if submit != "" {
		fmt.Printf("tapped %q\n", submit)
	}
	return true, nil
}

// unresolved reports the runner could not name a field on the screen at all
// ("X: no such field" / "X: matches N elements", JSON-RPC invalid params). The
// runner resolves fields against the foreground app's own tree, and a form in
// an SFSafariViewController web view is not in it: its content lives in
// SafariViewService, which only the full dump merges. The step path below reads
// that dump, so a form the runner cannot place is not a form we cannot fill.
// A mismatch after typing is an internal error, not this, so nothing retypes.
func unresolved(err error) bool {
	return err != nil &&
		(strings.Contains(err.Error(), "no such field") || strings.Contains(err.Error(), "-32602"))
}

// typeInto taps a field, proves it holds keyboard focus (a tap that leaves the
// keyboard on the previous field would type there), clears an old value, and
// types. Up to three taps, then it refuses rather than type blind.
func typeInto(c *devicekit.DeviceKitClient, f *types.ScreenElement, text string) error {
	typ := f.Type
	x, y := f.Rect.X+f.Rect.Width/2, f.Rect.Y+f.Rect.Height/2
	var els []types.ScreenElement
	var err error
	focused := false
	check := func() error {
		if els, err = dump(c); err != nil {
			return err
		}
		cur := focusedEl(els)
		// Not by rect: a web page scrolls the field when it takes focus.
		focused = cur != nil && cur.Type == typ && deref(cur.Label) == deref(f.Label) && deref(cur.Placeholder) == deref(f.Placeholder)
		return nil
	}
	for try := 0; try < 3 && !focused; try++ {
		if err := c.Tap(x, y); err != nil {
			return err
		}
		time.Sleep(time.Duration(80+150*try) * time.Millisecond)
		if err := check(); err != nil {
			return err
		}
	}
	// A web form in SafariViewService takes taps at the wrong place; the Tab
	// key walks its fields in order, so step forward until the target holds focus.
	for try := 0; try < 5 && !focused; try++ {
		if err := c.PressKeys([]devicekit.KeyCombo{{Key: "tab", Modifiers: []string{}}}); err != nil {
			return err
		}
		time.Sleep(200 * time.Millisecond)
		if err := check(); err != nil {
			return err
		}
	}
	if !focused {
		return fmt.Errorf("field did not take focus after 3 taps and 5 tabs; nothing typed")
	}
	cur := func() *types.ScreenElement {
		return find(els, func(e *types.ScreenElement) bool { return e.Focused != nil && *e.Focused && e.Type == typ })
	}
	// Empty the field first. Secure fields hide their value as dots, so the
	// count of dots is the count of backspaces (select-all is unavailable there).
	if e := cur(); e != nil {
		old := deref(e.Value)
		if old != "" && old != deref(e.Placeholder) {
			if err := c.PressKeys(backspaces(utf8.RuneCountInString(old))); err != nil {
				return err
			}
		}
	}
	if err := c.SendKeys(text); err != nil {
		return err
	}
	// Prove what landed: the exact text, or for a secure field the same
	// number of dots. A stray character made a sign-in fail (2026-10-10).
	// The tree lags typing by a beat, so re-read briefly before refusing.
	got := ""
	until(1500*time.Millisecond, 200*time.Millisecond, func() bool {
		if els, err = dump(c); err != nil {
			return true
		}
		got = ""
		if e := cur(); e != nil {
			got = deref(e.Value)
		}
		if typ == "SecureTextField" {
			return utf8.RuneCountInString(got) == utf8.RuneCountInString(text)
		}
		return got == text
	})
	if err != nil {
		return err
	}
	if typ == "SecureTextField" {
		if utf8.RuneCountInString(got) != utf8.RuneCountInString(text) {
			return fmt.Errorf("secure field holds %d characters, typed %d", utf8.RuneCountInString(got), utf8.RuneCountInString(text))
		}
	} else if got != text {
		return fmt.Errorf("field holds %q, typed %q", got, text)
	}
	return nil
}

// field finds one text field by its label, placeholder, identifier or value.
func field(els []types.ScreenElement, name string) (*types.ScreenElement, error) {
	var all []*types.ScreenElement
	walk(els, nil, func(e *types.ScreenElement, _ []*types.ScreenElement) {
		if fieldTypes[e.Type] {
			all = append(all, e)
		}
	})
	keys := func(e *types.ScreenElement) []string {
		return []string{deref(e.Label), deref(e.Placeholder), deref(e.Identifier), deref(e.Name), deref(e.Value)}
	}
	n := strings.ToLower(norm(name))
	for _, exact := range []bool{true, false} {
		var hit []*types.ScreenElement
		for _, e := range all {
			for _, k := range keys(e) {
				k = strings.ToLower(norm(k))
				if k != "" && (exact && k == n || !exact && strings.Contains(k, n)) {
					hit = append(hit, e)
					break
				}
			}
		}
		if len(hit) == 1 {
			return hit[0], nil
		}
		if len(hit) > 1 {
			return nil, fmt.Errorf("%q matches %d fields; name it more exactly", name, len(hit))
		}
	}
	var names []string
	for _, e := range all {
		names = append(names, fmt.Sprintf("%s %q", e.Type, firstNonEmpty(keys(e))))
	}
	return nil, fmt.Errorf("no field %q; fields on screen: %s", name, strings.Join(names, ", "))
}

func firstNonEmpty(ss []string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
