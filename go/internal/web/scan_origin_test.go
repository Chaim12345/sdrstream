package web

import (
	"testing"
	"time"
)

// Rows a scanner produces in this process are "live"; rows a new Server
// loads from the saved state file are "restored" history. A restored row
// that a scanner touches again becomes live. This is what stops persisted
// state from masquerading as current-session activity in the UI.
func TestScanStatusOriginLiveThenRestoredThenLiveAgain(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SDRSTREAM_STATE_DIR", dir)

	first := New("", "")
	row := ScanStatus{Station: "rx.example", FrequencyHz: 145500000, Mode: "fm", State: "decoded", Result: "ok", UpdatedAt: time.Now()}
	first.UpsertScanStatus(row)
	got := first.ScanStatuses()
	if len(got) != 1 || got[0].Origin != "live" {
		t.Fatalf("fresh upsert origin = %+v, want one live row", got)
	}
	first.flushScanState()

	second := New("", "")
	restored := second.ScanStatuses()
	if len(restored) != 1 || restored[0].Origin != "restored" {
		t.Fatalf("reloaded origin = %+v, want one restored row", restored)
	}
	if restored[0].State != "decoded" || restored[0].Result != "ok" {
		t.Fatalf("reloaded row lost its content: %+v", restored[0])
	}

	// A scanner touching the same station again marks it live.
	second.UpsertScanStatus(ScanStatus{Station: "rx.example", FrequencyHz: 145500000, Mode: "fm", State: "listening", UpdatedAt: time.Now()})
	again := second.ScanStatuses()
	if len(again) != 1 || again[0].Origin != "live" {
		t.Fatalf("re-touched row origin = %+v, want live", again)
	}
}
