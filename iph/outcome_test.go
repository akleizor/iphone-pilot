package main

import (
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/mobile-next/mobilecli/devices/devicekit"
)

func TestOutcomeSeparatesNotSentFromUnknown(t *testing.T) {
	// nothing listens: the dial fails, so the tap never left the Mac
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := ln.Addr().String()
	ln.Close()
	_, err := devicekit.NewDeviceKitClient(dead).CallRPCWithTimeout("device.io.tap", nil, time.Second)
	if code, _ := outcome(err); code != 1 {
		t.Fatalf("refused connection = exit %d, want 1 (not sent): %v", code, err)
	}
	// a listener that accepts and never answers: the request was sent
	ln, _ = net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()
	_, err = devicekit.NewDeviceKitClient(ln.Addr().String()).CallRPCWithTimeout("device.io.tap", nil, 300*time.Millisecond)
	if code, _ := outcome(err); code != exitUnknown {
		t.Fatalf("timeout after sending = exit %d, want %d (unknown): %v", code, exitUnknown, err)
	}
	if code, _ := outcome(fmt.Errorf("covered by the keyboard")); code != 1 {
		t.Fatal("an iph refusal is not applied")
	}
}
