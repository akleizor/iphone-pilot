package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// doctor never starts, re-signs, or installs an agent. A live RPC is distinct
// from the native developer connection and from a cached profile's validity.
func doctor(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("usage: iph doctor")
	}
	report := map[string]any{"device": deviceUUID, "agent": false, "developer_connection": false}
	if err := reachable(deviceUUID); err != nil {
		report["developer_error"] = err.Error()
	} else {
		report["developer_connection"] = true
	}
	if addr, ok := findAgent(); ok {
		report["agent"] = true
		report["agent_address"] = addr
	} else if err := ensureSigned(); err != nil {
		report["agent_error"] = err.Error()
	} else {
		report["agent_error"] = "runner not started; iph up starts it"
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		return err
	}
	if report["agent"] != true {
		return fmt.Errorf("phone control not verified; agent is not answering")
	}
	return nil
}

func remoteCommand(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: iph remote setup|pair|doctor|screenshot PATH|run [options]")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	skill := filepath.Clean(filepath.Join(filepath.Dir(exe), ".."))
	script := filepath.Join(skill, "scripts", "remote.py")
	python := filepath.Join(skill, ".runtime", "bin", "python")
	var cmd *exec.Cmd
	// Setup is explicit, uses a private venv, and never installs globally.
	if args[0] == "setup" {
		if len(args) != 1 {
			return fmt.Errorf("usage: iph remote setup")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
		defer cancel()
		cmd = exec.CommandContext(ctx, "bash", filepath.Join(skill, "scripts", "remote-setup.sh"))
	} else {
		if _, err := os.Stat(python); err != nil {
			return fmt.Errorf("remote dependency missing; run iph remote setup")
		}
		cmd = exec.Command(python, append([]string{script}, args...)...)
	}
	cmd.Env = append(os.Environ(), "IPH_DEVICE="+deviceUUID, "IPH_PORT="+port)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}
