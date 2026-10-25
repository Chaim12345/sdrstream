package web

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// The decode-targets list is the phone's answer to "show real decoded
// data": one tap puts the monitor on a window where decodable data
// modes actually live (FT8/NAVTEX/RTTY/...), sourced from the curated
// HF target list - never hand-maintained duplicates.
func TestDecodeTargetsServed(t *testing.T) {
	t.Setenv("SDRSTREAM_UI_PASSWORD", "")
	s := New(t.TempDir(), t.TempDir())
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/decode-targets.json", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	var out []DecodeTarget
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("json: %v", err)
	}
	if len(out) < 5 {
		t.Fatalf("only %d targets served", len(out))
	}
	seen := map[string]bool{}
	for _, tg := range out {
		if tg.Name == "" || tg.Kind == "" || tg.Mode == "" {
			t.Errorf("incomplete target: %+v", tg)
		}
		if tg.DialKHz <= 0 {
			t.Errorf("%s: no dial frequency", tg.Name)
		}
		if !(tg.StartHz < tg.EndHz) {
			t.Errorf("%s: bad window %v..%v", tg.Name, tg.StartHz, tg.EndHz)
		}
		dialHz := tg.DialKHz * 1e3
		if dialHz < tg.StartHz || dialHz > tg.EndHz {
			t.Errorf("%s: dial %.1f kHz outside its window", tg.Name, tg.DialKHz)
		}
		seen[tg.Kind] = true
	}
	// The decodable-data promise: at least FT8 and NAVTEX must be offered.
	if !seen["ft8"] || !seen["navtex"] {
		t.Errorf("missing core data modes: %v", seen)
	}
}

// The chip row must ship in the served shell and script - a handler
// without the UI half is an invisible feature.
func TestDecodeTargetsUIWired(t *testing.T) {
	if !strings.Contains(indexPage, `id="decode-targets"`) {
		t.Error("index page has no decode-targets container")
	}
	js, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(js), "initDecodeTargets") {
		t.Error("app.js does not render decode targets")
	}
}
