package main

// `iph setup [--device UUID] [--team TEAM_ID] [--prefix BUNDLE_PREFIX]` takes a
// Mac with Xcode and a trusted iPhone to a working agent in one command:
// it writes ~/.config/iph/devices.json when there is none, builds and signs
// the runner when its profile is missing or expired, starts it, and reports
// what works. Every step it can skip (already configured, still signed,
// already running) it skips.

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"syscall"
)

func setup(args []string) error {
	var uuid, team, prefix string
	for i := 0; i < len(args); i++ {
		if i+1 >= len(args) {
			return fmt.Errorf("%s needs a value", args[i])
		}
		switch args[i] {
		case "--device":
			uuid = args[i+1]
		case "--team":
			team = args[i+1]
		case "--prefix":
			prefix = args[i+1]
		default:
			return fmt.Errorf("usage: iph setup [--device UUID] [--team TEAM_ID] [--prefix BUNDLE_PREFIX]")
		}
		i++
	}

	// 1. which phone
	if len(cfg.Devices) == 0 {
		if uuid == "" {
			phones, err := trustedPhones()
			if err != nil {
				return err
			}
			switch len(phones) {
			case 0:
				return fmt.Errorf("no trusted iPhone found: connect it by USB once, tap Trust, turn on Developer Mode, then rerun")
			case 1:
				uuid = phones[0].id
				fmt.Printf("phone: %s (%s)\n", phones[0].name, uuid)
			default:
				for _, p := range phones {
					fmt.Printf("  %s  %s\n", p.id, p.name)
				}
				return fmt.Errorf("several iPhones are trusted; rerun with --device <UUID> from the list above")
			}
		}
		path, err := writeConfig(uuid)
		if err != nil {
			return err
		}
		fmt.Println("wrote", path)
		// the port and state paths derive from the device at start-up: rerun with it
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		return syscall.Exec(exe, os.Args, os.Environ())
	} else {
		fmt.Printf("phone: %s (from config)\n", deviceUUID)
	}

	// 2. the runner, built and signed only when it needs to be
	if ensureSigned() == nil {
		fmt.Println("runner: built and signed")
	} else {
		if team == "" {
			teams, err := developmentTeams()
			if err != nil || len(teams) != 1 {
				return fmt.Errorf("found signing teams %v; add your Apple ID in Xcode > Settings > Accounts, then rerun with --team <TEAM_ID>", teams)
			}
			team = teams[0]
		}
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		script := filepath.Join(filepath.Dir(exe), "..", "scripts", "build-runner.sh")
		cmd := exec.Command(script, team)
		if prefix != "" {
			cmd.Args = append(cmd.Args, prefix)
		}
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		fmt.Printf("runner: building and signing with team %s (a few minutes)\n", team)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("runner build failed: %w", err)
		}
	}

	// 3. start it and say what works
	addr, err := up()
	if err != nil {
		return err
	}
	fmt.Println("agent:", addr)
	fmt.Println("ready: try `iph launch com.apple.Preferences && iph dump`")
	return nil
}

type phone struct{ id, name string }

// trustedPhones lists the physical iPhones this Mac is paired with.
func trustedPhones() ([]phone, error) {
	f, err := os.CreateTemp("", "iph-devices-*.json")
	if err != nil {
		return nil, err
	}
	f.Close()
	defer os.Remove(f.Name())
	if out, err := exec.Command("xcrun", "devicectl", "list", "devices", "--json-output", f.Name()).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("xcrun devicectl list devices: %v: %s", err, out)
	}
	b, err := os.ReadFile(f.Name())
	if err != nil {
		return nil, err
	}
	return parsePhones(b)
}

func parsePhones(b []byte) ([]phone, error) {
	var r struct {
		Result struct {
			Devices []struct {
				Identifier string `json:"identifier"`
				Hardware   struct {
					DeviceType string `json:"deviceType"`
					Reality    string `json:"reality"`
				} `json:"hardwareProperties"`
				Connection struct {
					PairingState string `json:"pairingState"`
				} `json:"connectionProperties"`
				Props struct {
					Name string `json:"name"`
				} `json:"deviceProperties"`
			} `json:"devices"`
		} `json:"result"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	var out []phone
	for _, d := range r.Result.Devices {
		if d.Hardware.DeviceType == "iPhone" && d.Hardware.Reality == "physical" && d.Connection.PairingState == "paired" {
			out = append(out, phone{d.Identifier, d.Props.Name})
		}
	}
	return out, nil
}

// writeConfig creates the device file for one phone; it never overwrites one.
func writeConfig(uuid string) (string, error) {
	if !coreDeviceUUID.MatchString(uuid) {
		return "", fmt.Errorf("%q is not a CoreDevice UUID (the Identifier column of `xcrun devicectl list devices`)", uuid)
	}
	path := env("IPH_CONFIG", filepath.Join(os.Getenv("HOME"), ".config", "iph", "devices.json"))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	b, _ := json.MarshalIndent(config{Default: "phone", Devices: map[string]deviceConfig{"phone": {UUID: uuid}}}, "", "  ")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", err
	}
	defer f.Close()
	_, err = f.Write(append(b, '\n'))
	return path, err
}

// developmentTeams reads the team ids (the certificate's OU) of the Apple
// Development certificates in the keychain, which Xcode creates on sign-in.
func developmentTeams() ([]string, error) {
	out, err := exec.Command("security", "find-certificate", "-a", "-c", "Apple Development", "-p").Output()
	if err != nil {
		return nil, err
	}
	var teams []string
	for rest := out; ; {
		var blk *pem.Block
		if blk, rest = pem.Decode(rest); blk == nil {
			break
		}
		c, err := x509.ParseCertificate(blk.Bytes)
		if err != nil {
			continue
		}
		for _, ou := range c.Subject.OrganizationalUnit {
			if !slices.Contains(teams, ou) {
				teams = append(teams, ou)
			}
		}
	}
	return teams, nil
}
