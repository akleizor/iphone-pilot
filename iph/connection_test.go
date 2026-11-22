package main

import (
	"crypto/sha256"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExplicitAgentRefusesWithoutLocalFallback(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()
	t.Setenv("IPH_AGENT_ADDR", addr)
	if _, ok := findAgent(); ok {
		t.Fatal("closed endpoint reported alive")
	}
	if _, err := up(); err == nil || !strings.Contains(err.Error(), "no local fallback") {
		t.Fatalf("unexpected up result: %v", err)
	}
}

func TestMissingProfileDoesNotInstall(t *testing.T) {
	old := products
	products = filepath.Join(t.TempDir(), "missing")
	defer func() { products = old }()
	err := ensureSigned()
	if err == nil || !strings.Contains(err.Error(), "no installation attempted") ||
		!strings.Contains(err.Error(), "Apple Development") {
		t.Fatalf("expected ownership refusal: %v", err)
	}
}

func TestRemoteSetupRejectsExtraArgs(t *testing.T) {
	if err := remoteCommand([]string{"setup", "--unexpected"}); err == nil {
		t.Fatal("unexpected args accepted")
	}
}

func TestDoctorReportsMissingAgent(t *testing.T) {
	old := reachable
	reachable = func(string) error { return nil }
	defer func() { reachable = old }()
	t.Setenv("IPH_AGENT_ADDR", "127.0.0.1:1")
	out, err := os.CreateTemp(t.TempDir(), "doctor")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	original := os.Stdout
	os.Stdout = out
	defer func() { os.Stdout = original }()
	if err := doctor(nil); err == nil {
		t.Fatal("doctor succeeded without agent")
	}
	data, _ := os.ReadFile(out.Name())
	if !strings.Contains(string(data), `"agent":false`) || !strings.Contains(string(data), `"developer_connection":true`) ||
		!strings.Contains(string(data), `"agent_error"`) {
		t.Fatalf("wrong capabilities: %s", data)
	}
}

// Without a fresh helper beside iph, the OCR helper comes from the state dir: a Homebrew install dir is read-only.
func TestStateNotInInstallDir(t *testing.T) {
	old := stateDir
	stateDir = t.TempDir()
	defer func() { stateDir = old }()
	sum := sha256.Sum256([]byte(ocrSource))
	cached := filepath.Join(stateDir, fmt.Sprintf("iph-ocr-%x", sum[:6]))
	if err := os.WriteFile(cached, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if bin, err := ocrBinary(); err != nil || bin != cached {
		t.Fatalf("ocrBinary = %q, %v; want the state-dir helper %q", bin, err, cached)
	}
}
