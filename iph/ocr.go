package main

// Fast perception for `iph flow`. The runner streams the screen as MJPEG
// (/mjpeg, ~34 ms a frame at half scale); one long-lived connection keeps the
// latest frame in memory, so "look at the screen" costs nothing on the phone.
// A warm on-Mac Vision OCR process reads a frame in ~15 ms. Together a fresh
// read is ~50 ms, against ~0.85 s for the accessibility dump and ~0.2 s for a
// screenshot RPC. Screens OCR cannot decide (switches, empty fields, icon-only
// controls) fall back to the dump.

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/mobile-next/mobilecli/types"
)

// streamScale is the MJPEG frame size in percent: 50 keeps OCR accurate on
// list text (33 misread "localhost" as "localhust"), measured 2026-10-10.
const streamScale = 50

type ocrLine struct {
	T          string `json:"t"`
	X, Y, W, H int
}

type ocrProc struct {
	in  io.WriteCloser
	out *bufio.Scanner
	cmd *exec.Cmd
}

func startOCR() (*ocrProc, error) {
	bin, err := ocrBinary()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(bin)
	w, _ := cmd.StdinPipe()
	r, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<22)
	return &ocrProc{w, sc, cmd}, nil
}

func (p *ocrProc) close() { _ = p.in.Close(); _ = p.cmd.Process.Kill(); _ = p.cmd.Wait() }

// read OCRs one image and returns its lines in screen points.
func (p *ocrProc) read(img []byte, pxPerPoint float64) ([]ocrLine, error) {
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(img)))
	if _, err := p.in.Write(append(hdr[:], img...)); err != nil {
		return nil, err
	}
	if !p.out.Scan() {
		return nil, fmt.Errorf("iph-ocr exited")
	}
	var lines []ocrLine
	if err := json.Unmarshal(p.out.Bytes(), &lines); err != nil {
		return nil, err
	}
	for i := range lines {
		l := &lines[i]
		l.X, l.Y = int(float64(l.X)/pxPerPoint), int(float64(l.Y)/pxPerPoint)
		l.W, l.H = int(float64(l.W)/pxPerPoint), int(float64(l.H)/pxPerPoint)
	}
	return lines, nil
}

// frames holds the newest MJPEG frame from one long-lived stream connection.
type frames struct {
	mu   sync.Mutex
	cond *sync.Cond
	jpg  []byte
	seq  int
	err  error
	stop func()
}

func startFrames(addr string) (*frames, error) {
	f := &frames{}
	f.cond = sync.NewCond(&f.mu)
	req, _ := http.NewRequest("GET", fmt.Sprintf("http://%s/mjpeg?fps=30&quality=40&scale=%d", addr, streamScale), nil)
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return nil, err
	}
	f.stop = func() { resp.Body.Close() }
	go func() {
		r := bufio.NewReaderSize(resp.Body, 1<<18)
		var buf []byte
		chunk := make([]byte, 1<<16)
		for {
			n, err := r.Read(chunk)
			buf = append(buf, chunk[:n]...)
			for {
				s := bytes.Index(buf, []byte{0xff, 0xd8})
				if s < 0 {
					buf = buf[:0]
					break
				}
				e := bytes.Index(buf[s+2:], []byte{0xff, 0xd9})
				if e < 0 {
					buf = buf[s:]
					break
				}
				frame := append([]byte(nil), buf[s:s+2+e+2]...)
				buf = buf[s+2+e+2:]
				f.mu.Lock()
				f.jpg, f.seq = frame, f.seq+1
				f.cond.Broadcast()
				f.mu.Unlock()
			}
			if err != nil {
				f.mu.Lock()
				f.err = err
				f.cond.Broadcast()
				f.mu.Unlock()
				return
			}
		}
	}()
	return f, nil
}

// next returns a frame produced after the call began (never one captured
// before the last action), waiting at most 1 s.
func (f *frames) next() ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	want := f.seq + 1
	deadline := time.Now().Add(time.Second)
	for f.seq < want && f.err == nil {
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("no MJPEG frame in 1s")
		}
		go func() { time.Sleep(50 * time.Millisecond); f.cond.Broadcast() }()
		f.cond.Wait()
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.jpg, nil
}

// ocrElements turns OCR lines into synthetic tappable elements so the same
// table/describe/execute path serves both perception modes. The status bar
// (top 50 pt) is context, never a target.
func ocrElements(lines []ocrLine) []types.ScreenElement {
	// A page's large title is drawn far taller than its rows; tapping it does
	// nothing, and Jev wasted a step on "Settings" in every flow (2026-10-10).
	var hs []int
	for _, l := range lines {
		hs = append(hs, l.H)
	}
	slices.Sort(hs)
	median := 0
	if len(hs) > 0 {
		median = hs[len(hs)/2]
	}
	var els []types.ScreenElement
	for _, l := range lines {
		t := strings.TrimSpace(l.T)
		if t == "" {
			continue
		}
		typ := "Button"
		if l.Y < 50 || median > 0 && l.H*10 > median*18 && l.Y < 200 {
			typ = "StaticText"
		}
		s := t
		els = append(els, types.ScreenElement{Type: typ, Label: &s,
			Rect: types.ScreenElementRect{X: l.X, Y: l.Y, Width: max(l.W, 1), Height: max(l.H, 1)}})
	}
	types.AttachRefs(els)
	return els
}
