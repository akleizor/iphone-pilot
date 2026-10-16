package main

// `iph keep` keeps a phone's UI agent warm so no command waits for a cold
// start: it runs the XCTest runner and relaunches it whenever its ~5-minute
// test allowance lapses. It runs in the foreground; put it in tmux or a
// LaunchAgent.
//
// `iph build` compiles iph and its OCR helper into bin/.

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

//go:embed ocr.swift
var ocrSource string

func keep() error {
	if _, err := runnerCmd("keep-dd"); err != nil { // fail fast when unbuilt
		return err
	}
	_ = os.MkdirAll(devDir, 0o755)
	// Recorded like up()'s runner, so `--handback` ends it by its own pid.
	_ = os.WriteFile(keepPidFile, []byte(strconv.Itoa(os.Getpid())), 0o644)
	defer os.Remove(keepPidFile)
	var child *exec.Cmd
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		if child != nil && child.Process != nil {
			_ = child.Process.Signal(syscall.SIGTERM)
		}
		_ = os.Remove(keepPidFile)
		os.Exit(0)
	}()
	fmt.Printf("keeping %s's agent on port %s; log %s\n", deviceUUID, port, logFile)
	for {
		log, err := os.Create(logFile)
		if err != nil {
			return err
		}
		if child, err = runnerCmd("keep-dd"); err != nil {
			return err
		}
		child.Stdout, child.Stderr = log, log
		started := time.Now()
		_ = child.Run()
		log.Close()
		b, _ := os.ReadFile(logFile)
		txt := string(b)
		wait := 2 * time.Second
		switch {
		case strings.Contains(txt, "device is locked"), strings.Contains(txt, "Authentication canceled"):
			fmt.Println(time.Now().Format("15:04:05"), "phone locked or UI-testing passcode prompt cancelled; retry in 30s")
			wait = 30 * time.Second
		case strings.Contains(txt, "Unable to find a destination"):
			fmt.Println(time.Now().Format("15:04:05"), "phone not reachable by CoreDevice (needs Wi-Fi); retry in 30s")
			wait = 30 * time.Second
		case time.Since(started) < 20*time.Second:
			fmt.Println(time.Now().Format("15:04:05"), "runner exited at once; retry in 15s:", launchFailure(txt))
			wait = 15 * time.Second
		}
		time.Sleep(wait)
	}
}

// ocrBinary returns the OCR helper next to iph, compiling the embedded source
// once when it is missing or older than this iph build.
func ocrBinary() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	bin := filepath.Join(filepath.Dir(exe), "iph-ocr")
	if bi, err := os.Stat(bin); err == nil {
		if ei, err := os.Stat(exe); err == nil && !bi.ModTime().Before(ei.ModTime()) {
			return bin, nil
		}
	}
	return bin, buildOCR(bin)
}

func buildOCR(bin string) error {
	src := filepath.Join(os.TempDir(), fmt.Sprintf("iph-ocr-%d.swift", os.Getpid()))
	if err := os.WriteFile(src, []byte(ocrSource), 0o600); err != nil {
		return err
	}
	defer os.Remove(src)
	out, err := exec.Command("swiftc", "-O", "-o", bin, src).CombinedOutput()
	if err != nil {
		return fmt.Errorf("building iph-ocr: %v: %s", err, out)
	}
	return nil
}

// build compiles iph from its source and the OCR helper into bin/.
func build() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	skill := filepath.Clean(filepath.Join(filepath.Dir(exe), ".."))
	cmd := exec.Command("go", "build", "-o", filepath.Join(skill, "bin", "iph"), ".")
	cmd.Dir = filepath.Join(skill, "iph")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}
	if err := buildOCR(filepath.Join(skill, "bin", "iph-ocr")); err != nil {
		return err
	}
	fmt.Println("built bin/iph and bin/iph-ocr; no app was installed")
	return nil
}
