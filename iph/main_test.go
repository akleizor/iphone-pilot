package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mobile-next/mobilecli/types"
)

func TestSwitchStateAndRefLookup(t *testing.T) {
	on, off, label := "1", "0", "Grid"
	els := []types.ScreenElement{{Type: "Cell", Children: []types.ScreenElement{
		{Type: "Switch", Label: &label, Value: &on},
		{Type: "Switch", Label: &label, Value: &off},
	}}}
	types.AttachRefs(els)
	annotate(els)

	if got := onOff(findRef(els, "@e2")); got != "on" {
		t.Fatalf("@e2 = %s, want on", got)
	}
	if got := onOff(findRef(els, "@e3")); got != "off" {
		t.Fatalf("@e3 = %s, want off", got)
	}
	if findRef(els, "@e9") != nil {
		t.Fatal("missing ref should be nil")
	}
	if txt := types.FormatText(els); !strings.Contains(txt, "@e2 [Switch] \"Grid\" (checked)") {
		t.Fatalf("text dump lacks switch state:\n%s", txt)
	}
}

func TestBatchParsingGroupsChains(t *testing.T) {
	chains, err := parseSteps([]string{"open:com.apple.Preferences", "tap:General", "+", "tap:About", "dump"})
	if err != nil {
		t.Fatal(err)
	}
	if len(chains) != 3 {
		t.Fatalf("got %d chains, want 3: %v", len(chains), chains)
	}
	if len(chains[1]) != 2 || chains[1][0].target != "General" || chains[1][1].target != "About" {
		t.Fatalf("'+' must join into one chain: %v", chains[1])
	}
	// A joined chain shares one screen read; unjoined steps each get their own.
	if !needsScreen("tap") || needsScreen("open") || needsScreen("sleep") || needsScreen("dump") {
		t.Fatal("needsScreen misclassifies a step")
	}
}

func TestBatchStepOptions(t *testing.T) {
	c, err := parseSteps([]string{"press:General:1500", "wait:Reload:5", "gone:Loading:3", "swipe", "text:hello there"})
	if err != nil {
		t.Fatal(err)
	}
	if s := c[0][0]; s.target != "General" || s.dur != 1500 {
		t.Fatalf("press parsed as %+v", s)
	}
	if s := c[1][0]; s.target != "Reload" || s.dur != 5 {
		t.Fatalf("wait parsed as %+v", s)
	}
	if s := c[2][0]; s.target != "Loading" || s.dur != 3 {
		t.Fatalf("gone parsed as %+v", s)
	}
	if s := c[3][0]; s.target != "up" {
		t.Fatalf("bare swipe should default to up, got %+v", s)
	}
	if s := c[4][0]; s.op != "text" || s.text != "hello there" {
		t.Fatalf("text must keep everything after the first colon, got %+v", s)
	}
}

func TestBatchRejectsUnknownStep(t *testing.T) {
	if _, err := parseSteps([]string{"frobnicate:x"}); err == nil {
		t.Fatal("unknown verb must error")
	}
	if _, err := parseSteps([]string{"+", "tap:General"}); err == nil {
		t.Fatal("leading '+' must error")
	}
}

func TestProfileExpiryParsesInstalledAgentProfile(t *testing.T) {
	profile := products + "/Debug-iphoneos/devicekit-iosUITests-Runner.app/embedded.mobileprovision"
	if _, err := os.Stat(profile); err != nil {
		t.Skip("agent not built on this machine")
	}
	exp, err := profileExpiry(profile)
	if err != nil || exp.Year() < 2020 {
		t.Fatalf("profileExpiry = %v, %v", exp, err)
	}
}

func TestLabelMatchFoldsNoBreakSpacesAndSkipsNestedLabels(t *testing.T) {
	cell, text := "Face ID & Passcode", "Face ID & Passcode"
	els := []types.ScreenElement{{Type: "Button", Label: &cell, Children: []types.ScreenElement{{Type: "StaticText", Label: &text}}}}
	types.AttachRefs(els)
	el, err := resolveLabel(els, "Face ID & Passcode")
	if err != nil || el.Ref != "@e1" {
		t.Fatalf("got %v, %v; want the Button @e1 (its own label nested inside is not a second match)", el, err)
	}
}

func TestCoveredByIgnoresFullScreenContainers(t *testing.T) {
	search, row := "Search", "General"
	els := []types.ScreenElement{
		{Type: "Button", Label: &row, Rect: types.ScreenElementRect{X: 20, Y: 860, Width: 388, Height: 52}},
		{Type: "Toolbar", Rect: types.ScreenElementRect{Width: 428, Height: 926}, Children: []types.ScreenElement{
			{Type: "SearchField", Label: &search, Rect: types.ScreenElementRect{X: 28, Y: 860, Width: 372, Height: 28}},
		}},
	}
	types.AttachRefs(els)
	if o := coveredBy(els, &els[0], 214, 886); o == nil || o.Type != "SearchField" {
		t.Fatalf("want SearchField cover, got %v", o)
	}
	if o := coveredBy(els, &els[0], 214, 500); o != nil {
		t.Fatalf("full-screen Toolbar must not count as cover, got %v", o.Type)
	}
}

func TestStaleRefIsFollowedWhenUniqueAndRefusedOtherwise(t *testing.T) {
	refsFile = t.TempDir() + "/last-dump.json"
	a, b, c := "Wi-Fi", "Bluetooth", "Control Center"
	before := []types.ScreenElement{{Type: "Button", Label: &a}, {Type: "Button", Label: &b}}
	types.AttachRefs(before)
	remember(before)

	// Scrolled: Wi-Fi is now @e2 — follow it.
	after := []types.ScreenElement{{Type: "Button", Label: &c}, {Type: "Button", Label: &a}}
	types.AttachRefs(after)
	if el, err := resolveRef(after, "@e1"); err != nil || deref(el.Label) != "Wi-Fi" {
		t.Fatalf("want @e1 to follow Wi-Fi, got %v, %v", el, err)
	}
	// New screen: Wi-Fi gone — refuse instead of tapping Control Center.
	gone := []types.ScreenElement{{Type: "Button", Label: &c}}
	types.AttachRefs(gone)
	if _, err := resolveRef(gone, "@e1"); err == nil || !strings.Contains(err.Error(), "screen changed") {
		t.Fatalf("want refusal, got %v", err)
	}
}

const testPhone = "11111111-2222-3333-4444-555555555555"

func TestConfigRejectsAHardwareUDIDAsUUID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices.json")
	os.WriteFile(path, []byte(`{"devices":{"p":{"uuid":"`+testPhone+`","addrs":["10.0.0.2"]}}}`), 0o644)
	c := loadConfig(path)
	if c.Devices["p"].UUID != testPhone || c.Devices["p"].Addrs[0] != "10.0.0.2" {
		t.Fatalf("config not read: %+v", c)
	}
	if os.Getenv("IPH_CONFIG_CHILD") == "1" { // the bad file must exit, so run it in a child
		os.WriteFile(path, []byte(`{"devices":{"p":{"uuid":"00008150-000000000000001E"}}}`), 0o644)
		loadConfig(path)
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestConfigRejectsAHardwareUDIDAsUUID")
	cmd.Env = append(os.Environ(), "IPH_CONFIG_CHILD=1")
	if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(out), "not a CoreDevice UUID") {
		t.Fatalf("a hardware UDID as uuid was accepted: %v %s", err, out)
	}
}

func TestReachabilityQueriesTheDeviceNotTheDeviceList(t *testing.T) {
	uuid := testPhone
	args := detailsCmd(uuid, "/tmp/out.json").Args
	joined := strings.Join(args, " ")

	// `device info details` resolves --device and opens a tunnel on demand, so
	// it answers for a phone the device list still calls "disconnected".
	if !strings.Contains(joined, "devicectl device info details") {
		t.Fatalf("want a details query, got %q", joined)
	}
	if strings.Contains(joined, "list") {
		t.Fatalf("reachability must not go through the device list: %q", joined)
	}
	// The UUID must be the --device argument itself, not merely present.
	if i := slices.Index(args, "--device"); i < 0 || i+1 >= len(args) || args[i+1] != uuid {
		t.Fatalf("--device must carry %s, got %q", uuid, joined)
	}
	if i := slices.Index(args, "--json-output"); i < 0 || i+1 >= len(args) || args[i+1] != "/tmp/out.json" {
		t.Fatalf("details must be written where the caller asked: %q", joined)
	}
}

// details mirrors what `devicectl device info details` really returns: the full
// record for one device, with the CoreDevice identifier and a tunnel state that
// is authoritative only because the query itself just opened the tunnel.
func details(identifier, tunnel string) []byte {
	return []byte(`{"result":{"identifier":"` + identifier + `",
	 "hardwareProperties":{"udid":"00008150-000000000000001E"},
	 "deviceProperties":{"name":"Test iPhone"},
	 "connectionProperties":{"tunnelState":"` + tunnel + `","transportType":"localNetwork"}}}`)
}

func TestReachableFromTrustsTunnelStateNotTheZeroExit(t *testing.T) {
	phone := testPhone
	if err := reachableFrom(phone, details(phone, "connected")); err != nil {
		t.Fatalf("connected phone = %v; want reachable", err)
	}
	// devicectl exits 0 from its pairing record for a device that is long gone
	// (a real iPad Air 3 did, 2026-10-04), so the state must decide, not the exit.
	if err := reachableFrom(phone, details(phone, "unavailable")); err == nil ||
		!strings.Contains(err.Error(), "not reachable") {
		t.Fatalf("absent-but-paired device = %v; want a refusal", err)
	}
	// "unavailable" contains "available": the old substring test passed it.
	if err := reachableFrom(phone, details(phone, "disconnected")); err == nil {
		t.Fatal("a device with no tunnel must be refused")
	}
	// A record with no tunnel state at all must refuse, not read as connected.
	if err := reachableFrom(phone, []byte(`{"result":{"identifier":"`+phone+`"}}`)); err == nil ||
		!strings.Contains(err.Error(), "unreported") {
		t.Fatalf("missing tunnelState = %v; want an explicit refusal", err)
	}
	// The refusal states CoreDevice's answer, never a guess about the network.
	if err := reachableFrom(phone, details(phone, "unavailable")); err == nil ||
		strings.Contains(err.Error(), "Wi-Fi") {
		t.Fatalf("refusal = %v; want the tunnel state without a network guess", err)
	}
	if err := reachableFrom(phone, []byte("not json")); err == nil {
		t.Fatal("malformed devicectl output must error, not report reachable")
	}
}

func TestDeviceReachableFlagRefusesAnUnreachablePhone(t *testing.T) {
	saved := reachable
	defer func() { reachable = saved }()

	reachable = func(uuid string) error { return fmt.Errorf("device %s not reachable", uuid) }
	if err := run("device", []string{"--reachable"}); err == nil ||
		!strings.Contains(err.Error(), "not reachable") {
		t.Fatalf("--reachable on an unreachable phone = %v; want its refusal", err)
	}
	reachable = func(string) error { return nil }
	if err := run("device", []string{"--reachable"}); err != nil {
		t.Fatalf("--reachable on a reachable phone = %v; want the UUID printed", err)
	}
	// Bare `device` stays offline: it is also how a caller reads the UUID while
	// the phone is away, and only the build needs reachability.
	reachable = func(string) error { t.Fatal("bare 'device' must not query CoreDevice"); return nil }
	if err := run("device", nil); err != nil {
		t.Fatalf("bare device = %v", err)
	}
	reachable = func(string) error { return nil }
	if err := run("device", []string{"--bogus"}); err == nil {
		t.Fatal("an unknown device flag must error rather than be ignored")
	}
}

// fakeXcrun puts an `xcrun` on PATH that answers a details query the way the
// real one does — zero exit, record written to --json-output, tunnel state as
// given — and records the argv it was handed.
func fakeXcrun(t *testing.T, tunnel string) (argv func() string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "argv")
	script := "#!/bin/bash\nprintf '%s\\n' \"$*\" > " + log + "\n" +
		"out=\"\"; while [ $# -gt 0 ]; do [ \"$1\" = --json-output ] && out=\"$2\"; shift; done\n" +
		"[ -n \"$out\" ] && cat > \"$out\" <<'JSON'\n" +
		string(details("22222222-3333-4444-5555-666666666666", tunnel)) + "\nJSON\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "xcrun"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	return func() string {
		b, err := os.ReadFile(log)
		if err != nil {
			t.Fatalf("fake xcrun was never called: %v", err)
		}
		return strings.TrimSpace(string(b))
	}
}

// deviceReachable runs the query AND reads its answer: with the details record
// parsed but its verdict dropped, every other test here stayed green.
func TestDeviceReachableReadsTheRecordItAskedFor(t *testing.T) {
	phone := testPhone

	argv := fakeXcrun(t, "unavailable")
	if err := deviceReachable(phone); err == nil {
		t.Fatal("a zero exit with tunnel 'unavailable' must still be refused")
	}
	if got := argv(); !strings.Contains(got, "devicectl device info details --device "+phone) {
		t.Fatalf("xcrun got %q", got)
	}

	fakeXcrun(t, "connected")
	if err := deviceReachable(phone); err != nil {
		t.Fatalf("tunnel 'connected' = %v; want reachable", err)
	}
}

func TestPhoneCommandsStampLastUseUnlessUntracked(t *testing.T) {
	saved := lastUse
	lastUse = filepath.Join(t.TempDir(), "dev", "last-use")
	defer func() { lastUse = saved }()

	t.Setenv("IPH_UNTRACKED", "1")
	markUse("dump")
	if _, err := os.Stat(lastUse); err == nil {
		t.Fatal("an untracked owner's call must not count as use")
	}
	t.Setenv("IPH_UNTRACKED", "")
	markUse("help")
	if _, err := os.Stat(lastUse); err == nil {
		t.Fatal("help is not phone use")
	}
	markUse("tap")
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(lastUse, old, old); err != nil {
		t.Fatal(err)
	}
	markUse("dump")
	if st, err := os.Stat(lastUse); err != nil || time.Since(st.ModTime()) > time.Minute {
		t.Fatalf("a later command must refresh the stamp: %v", err)
	}
}

func TestScreenshotIsSavedSmallUnlessFull(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src.png")
	if out, err := exec.Command("sips", "-s", "format", "png", "-z", "2778", "1284",
		"/System/Library/Desktop Pictures/.thumbnails/Big Sur.heic", "--out", src).CombinedOutput(); err != nil {
		t.Skipf("no sample image: %s", out)
	}
	png, _ := os.ReadFile(src)
	small := filepath.Join(t.TempDir(), "s.jpg")
	if err := saveScreenshot(small, png, false); err != nil {
		t.Fatal(err)
	}
	out, _ := exec.Command("sips", "-g", "pixelHeight", "-g", "format", small).Output()
	if !strings.Contains(string(out), "pixelHeight: 1000") || !strings.Contains(string(out), "jpeg") {
		t.Fatalf("not a 1000 px JPEG: %s", out)
	}
	full := filepath.Join(t.TempDir(), "f.png")
	_ = saveScreenshot(full, png, true)
	if b, _ := os.ReadFile(full); len(b) != len(png) {
		t.Fatal("--full must keep the original bytes")
	}
}

// Hermex: a full-screen Toolbar sibling over a long scrolled list. The content
// extent (4900 pt) must not make the 956 pt Toolbar count as a bar.
func TestCoveredByFullScreenToolbarOverLongList(t *testing.T) {
	sw, far := "Bot Mode (beta)", "Require Face ID"
	els := []types.ScreenElement{
		{Type: "Switch", Label: &sw, Rect: types.ScreenElementRect{X: 36, Y: 276, Width: 371, Height: 28}},
		{Type: "Switch", Label: &far, Rect: types.ScreenElementRect{X: 36, Y: 4902, Width: 371, Height: 28}},
		{Type: "Toolbar", Rect: types.ScreenElementRect{Width: 440, Height: 956}},
	}
	types.AttachRefs(els)
	if o := coveredBy(els, &els[0], 377, 290); o != nil {
		t.Fatalf("full-screen Toolbar reported as cover: %v", o.Type)
	}
}
