package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/mobile-next/mobilecli/devices/devicekit"
	"github.com/mobile-next/mobilecli/types"
)

// A flow that finished is saved as its batch steps and rerun next time with no
// model (~0.5 s a step instead of ~3 s). Only runs whose every tap is found by
// its label are saved: started from another screen, such a replay stops at a
// wait instead of tapping blind, and the flow falls back to Jev.
var replayFile = filepath.Join(devDir, "replays.json")

func replayKey(o doOpts) string { return o.goal + "\x00" + o.until }

func loadReplays() map[string][]string {
	m := map[string][]string{}
	if b, err := os.ReadFile(replayFile); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

func storeReplay(o doOpts, steps []string) {
	m := loadReplays()
	if steps == nil {
		delete(m, replayKey(o))
	} else {
		m[replayKey(o)] = steps
	}
	if b, err := json.MarshalIndent(m, "", "  "); err == nil && os.MkdirAll(filepath.Dir(replayFile), 0o755) == nil {
		_ = os.WriteFile(replayFile, b, 0o644)
	}
}

// runSavedReplay reports whether a saved replay finished the goal. A replay
// that fails is forgotten, so the next run plans afresh.
func runSavedReplay(c *devicekit.DeviceKitClient, o doOpts) bool {
	steps := loadReplays()[replayKey(o)]
	if len(steps) == 0 || o.dryRun || len(o.values) > 0 {
		return false
	}
	t0 := time.Now()
	chains, err := parseSteps(steps)
	if err == nil {
		err = batch(c, chains)
	}
	if err == nil && o.until != "" {
		err = wait(c, []string{o.until, "3"})
	}
	if err != nil {
		fmt.Printf("saved replay failed (%v); planning with Jev\n", err)
		storeReplay(o, nil)
		return false
	}
	fmt.Printf("done: saved replay in %.1fs, no model (%d steps)\n", time.Since(t0).Seconds(), len(steps))
	return true
}

// labelAt is the label of the smallest tree element under (x, y) that names
// exactly one element, or "" when none does.
func labelAt(tree []types.ScreenElement, x, y int) string {
	best, area := "", 0
	walk(tree, nil, func(e *types.ScreenElement, _ []*types.ScreenElement) {
		r := e.Rect
		l := elLabel(e)
		if l == "" || x < r.X || y < r.Y || x >= r.X+r.Width || y >= r.Y+r.Height {
			return
		}
		if a := r.Width * r.Height; best == "" || a < area {
			if _, err := resolveLabel(tree, l); err == nil {
				best, area = l, a
			}
		}
	})
	return best
}
