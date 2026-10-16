package main

import (
	"context"
	"errors"
	"io"
	"net"
	"syscall"
)

// Exit codes for commands that change the phone:
//
//	0  applied
//	1  not applied: refused by iph or the runner, or never sent; safe to retry after a fresh read
//	3  unknown: the request may have reached the phone; read the screen before retrying
const exitUnknown = 3

var actions = map[string]bool{
	"tap": true, "doubletap": true, "longpress": true, "toggle": true, "drag": true, "pinch": true,
	"swipe": true, "text": true, "clear": true, "keys": true, "button": true, "launch": true,
	"open": true, "kill": true, "url": true, "clipboard": true, "rotate": true, "appearance": true,
	"batch": true, "fill": true, "flow": true,
}

// outcome classifies a failed action by where it failed: before the request
// left the Mac it was not applied; after, it may have been.
func outcome(err error) (code int, note string) {
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "dial" {
		return 1, "not sent: the agent did not answer; safe to retry"
	}
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &ne) && ne.Timeout() ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return exitUnknown, "outcome unknown: the request may have reached the phone; read the screen before retrying"
	}
	return 1, ""
}
