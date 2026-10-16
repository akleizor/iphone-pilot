package main

// --handback: the phone belongs to a person who is using it. The command
// remembers the app in front before it starts, and the moment it finishes
// (done, refused, or failed) it puts that app back and ends the UI-automation
// session, so the "Automation Running" overlay never outlives the work and
// nobody waits on the agent to decide it is done.

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mobile-next/mobilecli/devices/devicekit"
)

// takeHandback strips --handback from args and reports whether it was set.
func takeHandback(args []string) ([]string, bool) {
	on := slices.Contains(args, "--handback")
	return slices.DeleteFunc(slices.Clone(args), func(a string) bool { return a == "--handback" }), on
}

// withHandback runs fn, then restores the app that was in front and ends the
// automation session, whatever fn returned.
func withHandback(c *devicekit.DeviceKitClient, fn func() error) error {
	prev := ""
	if app, err := c.GetActiveAppInfo(); err == nil {
		prev = app.BundleID
	}
	err := fn()
	t0 := time.Now()
	if prev != "" && prev != "com.apple.springboard" {
		_ = launch(c, prev) // resumes, never relaunches: their place in the app survives
	}
	state := "left to its owner"
	if endAutomation() {
		state = "ended"
	}
	fmt.Printf("handback: %s in front, automation %s, %dms\n", prev, state, time.Since(t0).Milliseconds())
	return err
}

// endAutomation stops whatever this device's runner was started by: `iph up`
// (agent.pid) or `iph keep` (keep.pid, which forwards SIGTERM to its
// xcodebuild). Recorded PIDs only; the runner exits and iOS drops the overlay.
// A runner started by something else (a LaunchAgent) records neither and is left alone.
func endAutomation() bool {
	ended := down() == nil
	if b, err := os.ReadFile(keepPidFile); err == nil {
		if pid, _ := strconv.Atoi(strings.TrimSpace(string(b))); pid > 0 && pid != os.Getpid() {
			ended = syscall.Kill(pid, syscall.SIGTERM) == nil || ended
		}
	}
	return ended
}
