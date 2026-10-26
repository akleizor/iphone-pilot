package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/mobile-next/mobilecli/devices/devicekit"
)

// fakeForm is a runner serving one web view with a form: it answers the JSON-RPC
// calls typeInto makes (dump, tap, text, keys) the way the real runner does.
type fakeForm struct {
	mu        sync.Mutex
	typ       []string // field Types, top to bottom
	label     []string
	text      []string
	focus     int
	relabel   bool // a focused field is labelled by its value, as a web view does
	dropFirst bool // the first multi-character text call loses its last character
	calls     []string
	taps      int
}

func (f *fakeForm) serve(t *testing.T) *devicekit.DeviceKitClient {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var req struct {
			Method string
			Params json.RawMessage
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var result any = map[string]any{}
		switch req.Method {
		case "device.dump.ui":
			var kids []map[string]any
			for i := range f.typ {
				label, value := f.label[i], f.text[i]
				if f.typ[i] == "SecureTextField" {
					value = strings.Repeat("•", len([]rune(value)))
				}
				if f.relabel && i == f.focus {
					label = strings.Repeat("•", 8)
				}
				kids = append(kids, map[string]any{"type": "XCUIElementType" + f.typ[i], "label": label, "value": value,
					"hasFocus": i == f.focus, "rect": map[string]int{"x": 20, "y": 100 + 60*i, "width": 300, "height": 40}})
			}
			view := map[string]any{"type": "XCUIElementTypeWebView", "rect": map[string]int{"x": 0, "y": 0, "width": 400, "height": 800}, "children": kids}
			result = map[string]any{"type": "XCUIElementTypeOther", "rect": map[string]int{"x": 0, "y": 0, "width": 400, "height": 800}, "children": []any{view}}
		case "device.io.tap":
			f.taps++
			var p struct{ Y float64 }
			_ = json.Unmarshal(req.Params, &p)
			f.focus = int(p.Y-100) / 60
		case "device.io.text":
			var p struct{ Text string }
			_ = json.Unmarshal(req.Params, &p)
			f.calls = append(f.calls, p.Text)
			if rs := []rune(p.Text); f.dropFirst && len(rs) > 1 {
				p.Text, f.dropFirst = string(rs[:len(rs)-1]), false
			}
			f.text[f.focus] += p.Text
		case "device.io.keys":
			var p struct{ Keys []struct{ Key string } }
			_ = json.Unmarshal(req.Params, &p)
			for _, k := range p.Keys {
				if rs := []rune(f.text[f.focus]); k.Key == "backspace" && len(rs) > 0 {
					f.text[f.focus] = string(rs[:len(rs)-1])
				}
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
	}))
	t.Cleanup(srv.Close)
	return devicekit.NewDeviceKitClient(srv.Listener.Addr().String())
}

func TestTypeIntoRecognisesAFocusedFieldByFormOrder(t *testing.T) {
	f := &fakeForm{typ: []string{"TextField", "TextField"}, label: []string{"Username", "Google Authenticator"},
		text: []string{"", ""}, focus: -1, relabel: true}
	c := f.serve(t)
	els, _ := dump(c)
	code, err := field(els, "Google Authenticator")
	if err != nil {
		t.Fatal(err)
	}
	if err := typeInto(c, els, code, "123456"); err != nil {
		t.Fatalf("a focused field relabelled by the page was not recognised: %v", err)
	}
	if f.text[1] != "123456" || f.text[0] != "" {
		t.Fatalf("typed into the wrong field: %q", f.text)
	}
}

func TestTypeIntoRetypesOnceWhenAKeyIsDropped(t *testing.T) {
	f := &fakeForm{typ: []string{"SecureTextField"}, label: []string{"Password"}, text: []string{""}, focus: -1, dropFirst: true}
	c := f.serve(t)
	els, _ := dump(c)
	pw, _ := field(els, "Password")
	if err := typeInto(c, els, pw, "Passw0rd"); err != nil {
		t.Fatalf("a dropped key was not retried: %v", err)
	}
	if f.text[0] != "Passw0rd" {
		t.Fatalf("field holds %q", f.text[0])
	}
	if n := len(f.calls); n != 1+8 || f.calls[1] != "P" {
		t.Fatalf("want one full send then one rune per call, got %q", f.calls)
	}
}

func TestTypeIntoKeepsAlreadyFocusedField(t *testing.T) {
	f := &fakeForm{typ: []string{"TextField", "SecureTextField"}, label: []string{"Email", "Password"},
		text: []string{"", ""}, focus: 0}
	c := f.serve(t)
	els, _ := dump(c)
	email, err := field(els, "Email")
	if err != nil {
		t.Fatal(err)
	}
	if err := typeInto(c, els, email, "person@example.com"); err != nil {
		t.Fatal(err)
	}
	if f.taps != 0 || f.text[0] != "person@example.com" || f.text[1] != "" {
		t.Fatalf("want existing email focus preserved without tapping; taps=%d values=%q", f.taps, f.text)
	}
}
