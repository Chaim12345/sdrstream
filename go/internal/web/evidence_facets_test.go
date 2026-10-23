package web

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/feeds"
)

// The two proof facets must be recorded and exposed separately: the Go
// live quality gate's own verdict is not an independent verification, and
// neither may be collapsed into a single generic "verified" flag.
func TestEvidenceFacetsSidecarRoundTrip(t *testing.T) {
	dir := t.TempDir()
	meta := CaptureMetadata{
		FrequencyHz: 4583000, Mode: "rtty", Receiver: "websdr.test:8901",
		Detail: "VQNE STARKWIND", Kind: "data", Target: "DWD RTTY 4583",
		Started:     time.Date(2026, 10, 23, 0, 1, 49, 0, time.UTC),
		DurationSec: 240, Verified: true,
		GateVerdict: feeds.GatePassed, Crosscheck: feeds.CrosscheckVerified,
		CrosscheckRef: "DDK2 reference schedule",
	}
	if err := WriteCaptureMetadata(dir, "clip.wav", meta); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "clip.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"gate_verdict=passed", "crosscheck=verified", "crosscheck_ref=DDK2 reference schedule"} {
		if !strings.Contains(string(raw), key+"\n") && !strings.HasSuffix(string(raw), key) {
			t.Fatalf("sidecar missing %q:\n%s", key, raw)
		}
	}

	// Legacy writes (no facets) keep the exact legacy byte format: no new
	// keys may appear, so old fixtures and archives are untouched.
	legacy := CaptureMetadata{FrequencyHz: 1000, Mode: "cw", Receiver: "r", Verified: false,
		Started: meta.Started, DurationSec: 5}
	if err := WriteCaptureMetadata(dir, "old.wav", legacy); err != nil {
		t.Fatal(err)
	}
	rawOld, err := os.ReadFile(filepath.Join(dir, "old.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"gate_verdict=", "crosscheck="} {
		if strings.Contains(string(rawOld), key) {
			t.Fatalf("legacy sidecar unexpectedly contains %q:\n%s", key, rawOld)
		}
	}
}

// Captures parsed from sidecars carry the facets through to the JSON API,
// and legacy sidecars leave them empty (the UI shows "not recorded").
func TestEvidenceFacetsParsedFromSidecar(t *testing.T) {
	dir := t.TempDir()
	writeReceiverCapture(t, dir, "DATA_4583kHz_rtty_test_20261023T000149Z_1.wav", CaptureMetadata{
		FrequencyHz: 4583000, Mode: "rtty", Receiver: "websdr.test:8901",
		Detail: "decode", Kind: "data", Target: "DWD",
		Started: time.Date(2026, 10, 23, 0, 1, 49, 0, time.UTC), DurationSec: 10,
		Verified: true, GateVerdict: feeds.GatePassed,
		Crosscheck: feeds.CrosscheckMismatch, CrosscheckRef: "second receiver",
	})
	s := &Server{capturesDir: dir}
	captures := s.listCaptures()
	if len(captures) != 1 {
		t.Fatalf("want 1 capture, got %d", len(captures))
	}
	c := captures[0]
	if c.GateVerdict != feeds.GatePassed || c.Crosscheck != feeds.CrosscheckMismatch || c.CrosscheckRef != "second receiver" {
		t.Fatalf("facets lost: %+v", c)
	}
	js, _ := json.Marshal(c)
	for _, key := range []string{`"gate_verdict":"passed"`, `"crosscheck":"mismatch"`, `"crosscheck_ref":"second receiver"`} {
		if !strings.Contains(string(js), key) {
			t.Fatalf("API JSON missing %s: %s", key, js)
		}
	}
}

// Ledger entries keep the facets; a legacy verified capture lets the gate
// facet be inferred (Verified meant "live gate passed") while a cross-check
// is never invented.
func TestReceiverLedgerEntryFacets(t *testing.T) {
	explicit := receiverLedgerEntry(Capture{
		Path: "a.wav", Kind: "data", Verified: true,
		GateVerdict: feeds.GateFailed, Crosscheck: feeds.CrosscheckVerified, CrosscheckRef: "ref",
	}, time.Now())
	if explicit.GateVerdict != feeds.GateFailed {
		t.Fatalf("explicit gate verdict overridden: %q", explicit.GateVerdict)
	}
	if explicit.Crosscheck != feeds.CrosscheckVerified || explicit.CrosscheckRef != "ref" {
		t.Fatalf("crosscheck facet lost: %+v", explicit)
	}

	legacy := receiverLedgerEntry(Capture{Path: "b.wav", Kind: "data", Verified: true}, time.Now())
	if legacy.GateVerdict != feeds.GatePassed {
		t.Fatalf("legacy verified capture should infer gate passed, got %q", legacy.GateVerdict)
	}
	if legacy.Crosscheck != "" {
		t.Fatalf("cross-check must never be inferred, got %q", legacy.Crosscheck)
	}

	legacyRejected := receiverLedgerEntry(Capture{Path: "c.wav", Kind: "data", Verified: false}, time.Now())
	if legacyRejected.GateVerdict != "" {
		t.Fatalf("legacy rejection is ambiguous (failed vs not recorded); got %q", legacyRejected.GateVerdict)
	}
}
