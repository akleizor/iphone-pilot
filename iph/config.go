package main

// Per-machine device settings live in ~/.config/iph/devices.json (IPH_CONFIG
// overrides), so nothing about one owner's phones is compiled in:
//
//	{
//	  "default": "test",
//	  "devices": {
//	    "test":  {"uuid": "11111111-…", "addrs": ["127.0.0.1:18600"]},
//	    "daily": {"uuid": "22222222-…", "udid": "00008150-…", "addrs": ["100.64.0.7"]}
//	  }
//	}
//
// uuid is the CoreDevice UUID (`xcrun devicectl list devices` → Identifier).
// udid is needed only where xcodebuild rejects the UUID as a destination.
// addrs are fixed routes to the device's agent ("host" or "host:port") tried
// before a LAN scan.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// A CoreDevice UUID is an RFC-4122 UUID; a hardware UDID is 24 hex digits with
// one dash (00008150-000000000000001E). devicectl accepts either, but iph must
// hold one form: it derives the device's port (crc32 of the string) and its
// state directory from it, so one phone written both ways would split into two
// ports and two caches. The `devicectl list devices` table shows the UDID, so
// it is what a reader is most likely to paste in.
var coreDeviceUUID = regexp.MustCompile(`^[0-9A-F]{8}(-[0-9A-F]{4}){3}-[0-9A-F]{12}$`)

type deviceConfig struct {
	UUID  string   `json:"uuid"`
	UDID  string   `json:"udid,omitempty"`
	Addrs []string `json:"addrs,omitempty"`
}

type config struct {
	Default string                  `json:"default"`
	Devices map[string]deviceConfig `json:"devices"`
}

var cfg = loadConfig(env("IPH_CONFIG", filepath.Join(os.Getenv("HOME"), ".config", "iph", "devices.json")))

// loadConfig reads the device file; a missing file is an empty config, a broken
// one stops iph rather than silently driving the wrong phone.
func loadConfig(path string) config {
	var c config
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return c
	}
	if err == nil {
		err = json.Unmarshal(b, &c)
	}
	for name, d := range c.Devices {
		if err == nil && !coreDeviceUUID.MatchString(d.UUID) {
			err = fmt.Errorf("device %q: uuid %q is not a CoreDevice UUID (a hardware UDID belongs in \"udid\")", name, d.UUID)
		}
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "iph: %s: %v\n", path, err)
		os.Exit(2)
	}
	return c
}

// device resolves a name from the config (case-insensitive) to its CoreDevice
// UUID; anything else is taken as a UUID already.
func device(v string) string {
	for name, d := range cfg.Devices {
		if strings.EqualFold(name, v) {
			return d.UUID
		}
	}
	return v
}

// deviceEntry is the config entry for a UUID, if any.
func deviceEntry(uuid string) deviceConfig {
	for _, d := range cfg.Devices {
		if d.UUID == uuid {
			return d
		}
	}
	return deviceConfig{}
}
