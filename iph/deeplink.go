package main

// Deep links skip navigation entirely: a Settings page or an app screen opens
// in one hop (~0.4 s) instead of tap-by-tap. The agent's device.url opens
// http(s) fine but drops Settings' prefs: URLs on the root page, so prefs:
// goes through CoreDevice's launch-with-payload, which honours the path.

import (
	"encoding/json"
	"os/exec"
	"strings"
	"time"
	"unicode"

	"github.com/mobile-next/mobilecli/devices/devicekit"
	"github.com/mobile-next/mobilecli/types"
)

func openURL(c *devicekit.DeviceKitClient, u string) error {
	// No --terminate-existing: a running Settings takes the link in ~0.23 s;
	// a relaunch costs ~0.5 s plus ~1 s before it can be read. CoreDevice
	// unreachable: fall back to the agent.
	if strings.HasPrefix(strings.ToLower(u), "prefs:") && exec.Command("xcrun", "devicectl", "device", "process", "launch",
		"--device", deviceUUID, "--payload-url", u, "com.apple.Preferences").Run() == nil {
		return nil
	}
	before := ""
	if app, err := c.GetActiveAppInfo(); err == nil {
		before = app.BundleID
	}
	if err := c.OpenURL(u); err != nil {
		return err
	}
	// Return once another app is in front, so the next read is not the old app's
	// screen; a link the front app handles itself leaves it there (3 s at most).
	until(3*time.Second, 100*time.Millisecond, func() bool {
		app, err := c.GetActiveAppInfo()
		return err != nil || app.BundleID != before
	})
	return nil
}

// settingsPages maps a Settings page's visible title to its deep link. When a
// goal names one, `iph flow` opens it directly and Jev only handles whatever is
// left (often nothing). Titles as iOS shows them; checked on iOS 27.
var settingsPages = []struct{ title, url string }{
	{"About", "prefs:root=General&path=About"},
	{"Keyboards", "prefs:root=General&path=Keyboard"},
	{"Keyboard", "prefs:root=General&path=Keyboard"},
	{"Software Update", "prefs:root=General&path=SOFTWARE_UPDATE_LINK"},
	{"iPhone Storage", "prefs:root=General&path=STORAGE_MGMT"},
	{"Date & Time", "prefs:root=General&path=DATE_AND_TIME"},
	{"Language & Region", "prefs:root=General&path=INTERNATIONAL"},
	{"VPN & Device Management", "prefs:root=General&path=ManagedConfigurationList"},
	{"General", "prefs:root=General"},
	{"Display & Brightness", "prefs:root=DISPLAY"},
	{"Battery", "prefs:root=BATTERY_USAGE"},
	{"Wi-Fi", "prefs:root=WIFI"},
	{"Bluetooth", "prefs:root=Bluetooth"},
	{"Cellular", "prefs:root=MOBILE_DATA_SETTINGS_ID"},
	{"Notifications", "prefs:root=NOTIFICATIONS_ID"},
	{"Sounds & Haptics", "prefs:root=Sounds"},
	{"Privacy & Security", "prefs:root=Privacy"},
	{"Accessibility", "prefs:root=ACCESSIBILITY"},
	{"Screen Time", "prefs:root=SCREEN_TIME"},
	{"Focus", "prefs:root=DO_NOT_DISTURB"},
}

// deepLinkFor returns the Settings page the goal names last (the destination:
// "General, then About" ends on About), or "". On a tie in position the longer
// title wins, so "Keyboards" is not read as "Keyboard" plus noise.
func deepLinkFor(goal string) (string, string) {
	g := strings.ToLower(goal)
	best, url, at := "", "", -1
	for _, p := range settingsPages {
		t := strings.ToLower(p.title)
		i := strings.Index(g, t) // first mention: "the About page" later repeats it
		if i < 0 {
			continue
		}
		if i > at || i == at && len(t) > len(best) {
			best, url, at = p.title, p.url, i
		}
	}
	return best, url
}

// goalEndsOn reports the goal's stop condition is just "that page is visible":
// no action anywhere in it ("Turn on Dark Mode in Display & Brightness" is not
// done on arrival), and no further page after it ("Accessibility, then Display
// & Text Size" must go on even when that page has no deep link).
func goalEndsOn(goal, title string) bool {
	g := strings.ToLower(goal)
	i := strings.Index(g, strings.ToLower(title))
	if i < 0 {
		return false
	}
	// Whole words: "set" must not match inside "settings".
	words := func(s string) []string {
		return strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) })
	}
	for _, w := range words(g) {
		switch w {
		case "tap", "turn", "toggle", "switch", "set", "type", "enter", "select", "choose", "enable", "disable", "change", "delete", "add":
			return false
		}
	}
	for _, w := range words(g[i+len(title):]) {
		switch w {
		case "then", "open", "go", "navigate", "into":
			return false
		}
	}
	return true
}

// navTitleIs waits until the navigation bar shows title. On a runner with
// device.find it is one on-device query (~0.15 s); otherwise a tree read.
func navTitleIs(c *devicekit.DeviceKitClient, title string, within time.Duration) bool {
	if ok, supported := findOnDevice(c, title, "navigationBar", within); supported {
		return ok
	}
	return until(within, 150*time.Millisecond, func() bool {
		els, err := dump(c)
		if err != nil {
			return false
		}
		nb := find(els, func(e *types.ScreenElement) bool { return e.Type == "NavigationBar" })
		return nb != nil && (strings.EqualFold(deref(nb.Label), title) || strings.EqualFold(deref(nb.Identifier), title))
	})
}

// findOnDevice asks the runner for matches of text (device.find), waiting up
// to within. supported is false on runners without the method.
func findOnDevice(c *devicekit.DeviceKitClient, text, typ string, within time.Duration) (ok, supported bool) {
	raw, err := c.CallRPC("device.find", map[string]any{"text": text, "type": typ, "wait": within.Seconds(), "limit": 1})
	if err != nil {
		return false, !unsupported(err)
	}
	var r struct {
		Matches []json.RawMessage `json:"matches"`
	}
	_ = json.Unmarshal(raw, &r)
	return len(r.Matches) > 0, true
}

// unsupported reports the runner predates an RPC (JSON-RPC "method not found").
func unsupported(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "ethod not found") || strings.Contains(err.Error(), "-32601"))
}

// aboutSettings reports the goal targets the Settings app: it names Settings,
// or Settings is the app in front.
func aboutSettings(c *devicekit.DeviceKitClient, goal string) bool {
	front := ""
	if app, err := c.GetActiveAppInfo(); err == nil {
		front = app.BundleID
	}
	return settingsGoal(goal, front)
}

// settingsGoal: with another app in front, "settings" means that app's own
// settings ("Hermex Settings > Notifications" jumped to iOS Settings,
// 2026-10-16) unless the goal names iOS Settings itself.
func settingsGoal(goal, front string) bool {
	g := strings.ToLower(goal)
	switch {
	case front == "com.apple.Preferences":
		return true
	case !strings.Contains(g, "setting"):
		return false
	case front == "" || front == "com.apple.springboard":
		return true
	}
	for _, s := range []string{"ios settings", "iphone settings", "settings app", "system settings"} {
		if strings.Contains(g, s) {
			return true
		}
	}
	return false
}
