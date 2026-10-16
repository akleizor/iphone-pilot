package main

import (
	"os"
	"os/exec"
	"strconv"
	"testing"

	"github.com/mobile-next/mobilecli/types"
)

func sp(s string) *string { return &s }

func TestTableIndexesActionableOnly(t *testing.T) {
	off := false
	els := []types.ScreenElement{
		{Type: "Button", Label: sp("General"), Rect: types.ScreenElementRect{X: 0, Y: 100, Width: 300, Height: 44}},
		{Type: "Switch", Label: sp("Bot Mode (beta)"), Rect: types.ScreenElementRect{X: 0, Y: 200, Width: 300, Height: 44},
			Children: []types.ScreenElement{{Type: "Switch", Label: sp("0"), Rect: types.ScreenElementRect{X: 250, Y: 200, Width: 50, Height: 30}}}},
		{Type: "SecureTextField", Placeholder: sp("Password"), Rect: types.ScreenElementRect{X: 0, Y: 300, Width: 300, Height: 44}},
		{Type: "Button", Label: sp("Disabled"), Enabled: &off, Rect: types.ScreenElementRect{X: 0, Y: 400, Width: 300, Height: 44}},
		{Type: "Button", Label: sp("Offscreen"), Rect: types.ScreenElementRect{X: 0, Y: 2000, Width: 300, Height: 44}},
		{Type: "StaticText", Label: sp("Context line"), Rect: types.ScreenElementRect{X: 0, Y: 500, Width: 300, Height: 20}},
	}
	items, ctx := table(els, 900)
	got := map[string]string{}
	for _, it := range items {
		got[elLabel(it.el)] = it.kind
	}
	want := map[string]string{"General": "TAP", "Bot Mode (beta)": "TOGGLE", "Password": "TYPE_TEXT"}
	if len(got) != len(want) {
		t.Fatalf("table = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%q kind = %q, want %q (all %v)", k, got[k], v, got)
		}
	}
	if len(ctx) != 1 || ctx[0] != "Context line" {
		t.Fatalf("context = %v", ctx)
	}
}

func TestValidChoiceRejectsUnknownAndBadSums(t *testing.T) {
	ids := map[string]bool{"1": true, "2": true}
	if err := validChoice(jevAnswer{"1", 0.9, map[string]float64{"1": 0.9, "2": 0.1}}, ids); err != nil {
		t.Fatal(err)
	}
	for _, a := range []jevAnswer{
		{"3", 0.9, map[string]float64{"1": 0.9, "2": 0.1}},
		{"1", 0.9, map[string]float64{"1": 0.5, "2": 0.1}},
		{"1", 0.9, map[string]float64{"1": 1.0}},
	} {
		if validChoice(a, ids) == nil {
			t.Fatalf("accepted %+v", a)
		}
	}
}

func TestParseDoSecretFromEnv(t *testing.T) {
	t.Setenv("PW_TEST", "s3cret")
	o, err := parseDo([]string{"sign in", "--secret", "password=PW_TEST", "--value", "url=https://x"})
	if err != nil || o.goal != "sign in" || len(o.values) != 2 || o.values[0].text != "s3cret" {
		t.Fatalf("parseDo = %+v, %v", o, err)
	}
	if _, err := parseDo([]string{"x", "--secret", "p=UNSET_VAR_XYZ"}); err == nil {
		t.Fatal("empty secret env accepted")
	}
}

func TestRiskyAndLooping(t *testing.T) {
	for _, l := range []string{"Sign Out of This Server", "Delete Profile", "Buy", "Erase All Content"} {
		if !risky(l) {
			t.Fatalf("%q not risky", l)
		}
	}
	for _, l := range []string{"Settings", "Bot Mode (beta)", "Add Server", "Payload"} {
		if risky(l) {
			t.Fatalf("%q flagged risky", l)
		}
	}
	h := []doneStep{{"a", true}, {"b", false}, {"c", false}, {"d", false}, {"e", false}}
	if !looping(h, 4) || looping(h[:4], 4) {
		t.Fatal("looping wrong")
	}
}

func TestOCRLargeTitleIsNotATarget(t *testing.T) {
	els := ocrElements([]ocrLine{
		{T: "Settings", X: 20, Y: 110, W: 130, H: 32},
		{T: "General", X: 60, Y: 400, W: 80, H: 15},
		{T: "Battery", X: 60, Y: 450, W: 80, H: 15},
		{T: "12:13", X: 60, Y: 10, W: 40, H: 12},
	})
	for _, e := range els {
		if deref(e.Label) == "Settings" && e.Type == "Button" || deref(e.Label) == "12:13" && e.Type == "Button" {
			t.Fatalf("%q offered as a target", deref(e.Label))
		}
	}
}

func TestNamedAndTapped(t *testing.T) {
	g := "Open General, then Keyboard. Stop when the Keyboards settings page is visible."
	if !named(g, "Keyboard") || !named(g, "General") || named(g, "On") || named(g, "Battery") {
		t.Fatal("named wrong")
	}
	if !tapped([]doneStep{{Action: `tapped "General"`}}, "General") {
		t.Fatal("tapped wrong")
	}
}

func TestFieldMatchesPlaceholderAndRefusesAmbiguity(t *testing.T) {
	u, p, id := "Username", "Password", "hermes-connection-address"
	els := []types.ScreenElement{
		{Type: "TextField", Identifier: &id, Rect: types.ScreenElementRect{Y: 300, Height: 22}},
		{Type: "TextField", Placeholder: &u, Rect: types.ScreenElementRect{Y: 350, Height: 22}},
		{Type: "SecureTextField", Placeholder: &p, Rect: types.ScreenElementRect{Y: 400, Height: 22}},
	}
	for name, want := range map[string]string{"Username": "TextField", "password": "SecureTextField", "address": "TextField"} {
		f, err := field(els, name)
		if err != nil || f.Type != want {
			t.Fatalf("field(%q) = %v, %v", name, f, err)
		}
	}
	if _, err := field(els, "e"); err == nil {
		t.Fatal("ambiguous substring accepted")
	}
}

func TestSimilarToleratesOCRNoise(t *testing.T) {
	a := "Settings\nWi-Fi\nBluetooth\n(Ii)) Cellular\nBattery\nVPN\nGeneral\nAccessibility\nCamera\nDisplay & Brightness"
	b := "Settings\nWi-Fi\nBluetooth\ni (Iil) Cellular\nBattery\nVPN\nGeneral\nAccessibility\nCamera\nDisplay & Brightness"
	c := "General\nAbout\nSoftware Update\niPhone Storage\nAirDrop"
	if similar(a, b) < 0.8 || similar(a, c) >= 0.8 {
		t.Fatalf("similar: same page %.2f, other page %.2f", similar(a, b), similar(a, c))
	}
}

func TestDeepLinkForPicksMostSpecificPage(t *testing.T) {
	for goal, want := range map[string]string{
		"Open General, then Keyboard. Stop when the Keyboards settings page is visible.": "prefs:root=General&path=Keyboard",
		"Open General, then About. Stop when the About page is visible.":                 "prefs:root=General&path=About",
		"Open Display & Brightness and stop when its page is visible.":                   "prefs:root=DISPLAY",
		"Turn on Bot Mode in Hermex":                                                     "",
	} {
		if _, got := deepLinkFor(goal); got != want {
			t.Fatalf("deepLinkFor(%q) = %q, want %q", goal, got, want)
		}
	}
}

func TestGoalEndsOn(t *testing.T) {
	if !goalEndsOn("Open General, then About. Stop when the About page is visible.", "About") {
		t.Fatal("About goal should end on About")
	}
	if !goalEndsOn("Open Battery in Settings and stop when the Battery page is visible.", "Battery") {
		t.Fatal(`"set" inside "settings" must not count as an action`)
	}
	if goalEndsOn("Open Display & Brightness and turn on Dark mode", "Display & Brightness") {
		t.Fatal("a goal with an action after the page must not end there")
	}
	if goalEndsOn("Turn on Dark Mode in Display & Brightness", "Display & Brightness") {
		t.Fatal("an action before the page name still has to be done")
	}
	if goalEndsOn("In Settings, open Accessibility, then Display & Text Size", "Accessibility") {
		t.Fatal("a later page with no deep link must not end the goal on the first page")
	}
	if !goalEndsOn("Open General, then its About page", "About") {
		t.Fatal("ending on the last named page is still done")
	}
}

func TestTakeHandback(t *testing.T) {
	args, on := takeHandback([]string{"Open Battery", "--handback", "--steps", "4"})
	if !on || len(args) != 3 || args[1] != "--steps" {
		t.Fatalf("takeHandback = %v, %v", args, on)
	}
	if _, on := takeHandback([]string{"x"}); on {
		t.Fatal("handback without the flag")
	}
}

func TestEndAutomationStopsRecordedKeeper(t *testing.T) {
	dir := t.TempDir()
	pidFile, keepPidFile = dir+"/agent.pid", dir+"/keep.pid"
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(keepPidFile, []byte(strconv.Itoa(cmd.Process.Pid)), 0o644)
	if !endAutomation() {
		t.Fatal("recorded keeper not reported ended")
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("keeper was not signalled")
	}
}

func TestSettingsGoal(t *testing.T) {
	cases := []struct {
		goal, front string
		want        bool
	}{
		{"In Hermex Settings > Notifications, turn on push", "com.uzairansar.hermesmobile", false},
		{"Open iOS Settings > Notifications", "com.uzairansar.hermesmobile", true},
		{"Open Settings > General > About", "com.apple.springboard", true},
		{"Open Notifications", "com.apple.Preferences", true},
		{"Open Notifications in Slack", "com.tinyspeck.chatlyio", false},
	}
	for _, c := range cases {
		if got := settingsGoal(c.goal, c.front); got != c.want {
			t.Errorf("settingsGoal(%q, %q) = %v, want %v", c.goal, c.front, got, c.want)
		}
	}
}

func TestUntilSeen(t *testing.T) {
	if !untilSeen("push on", "Notifications\nPush On · Current server") || untilSeen("Push on", "Push off") || untilSeen("", "anything") {
		t.Fatal("untilSeen wrong")
	}
}
