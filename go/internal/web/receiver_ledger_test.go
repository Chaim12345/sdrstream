package web

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/common"
	"github.com/chaim12345/sdrstream/go/internal/feeds"
)

// receiverLedgerServer builds a server whose captures directory and proof
// ledger live in temp dirs, mirroring the production wiring in main.go.
func receiverLedgerServer(t *testing.T, capturesDir, ledgerPath string) (*Server, *feeds.Ledger) {
	t.Helper()
	s := &Server{capturesDir: capturesDir}
	m := feeds.New(10, func(string, ...any) {})
	l := feeds.NewLedger(ledgerPath, 0)
	m.SetLedger(l)
	s.SetFeeds(m)
	return s, l
}

func writeReceiverCapture(t *testing.T, dir, name string, meta CaptureMetadata) {
	t.Helper()
	if err := common.WriteWAVFile(filepath.Join(dir, name), []int16{1, 2, 3}, 8000); err != nil {
		t.Fatal(err)
	}
	if err := WriteCaptureMetadata(dir, name, meta); err != nil {
		t.Fatal(err)
	}
}

// The core contract: every archived receiver capture lands exactly one
// source="receiver" ledger entry, so the entry count equals the
// capture-success count /api/metrics.json reports.
func TestReceiverCaptureLedgerMatchesMetrics(t *testing.T) {
	capturesDir := t.TempDir()
	ledgerPath := filepath.Join(t.TempDir(), "sdrstream-ledger.jsonl")
	started := time.Date(2026, 10, 23, 0, 10, 0, 0, time.UTC)
	writeReceiverCapture(t, capturesDir, "DATA_7074kHz_ft8_kiwi_test_8073_20261023T001000Z_1.wav", CaptureMetadata{
		FrequencyHz: 7074000, Mode: "ft8", Receiver: "kiwi.test:8073",
		Detail: "CQ DL1ABC JO31", Kind: "data", Target: "FT8 40m",
		Started: started, DurationSec: 15, Verified: true,
	})
	writeReceiverCapture(t, capturesDir, "VOICE_3885kHz_usb_websdr_test_20261023T001100Z_2.wav", CaptureMetadata{
		FrequencyHz: 3885000, Mode: "usb", Receiver: "websdr.test:8901",
		Detail: "weather forecast for the coastal waters", Kind: "voice",
		Started: started.Add(time.Minute), DurationSec: 12, Verified: true,
	})
	// Record-all keeps clips the live gate rejected; they are successful
	// captures too and must be ledgered with the honest gate verdict.
	writeReceiverCapture(t, capturesDir, "NOISE_10136kHz_cw_kiwi_test_8073_20261023T001200Z_3.wav", CaptureMetadata{
		FrequencyHz: 10136000, Mode: "cw", Receiver: "kiwi.test:8073",
		Kind: "carrier", Gate: "no decodable content", Started: started.Add(2 * time.Minute),
		DurationSec: 10, Verified: false,
	})

	s, l := receiverLedgerServer(t, capturesDir, ledgerPath)
	got := s.scanCaptures()
	if len(got) != 3 {
		t.Fatalf("scanCaptures = %d, want 3", len(got))
	}
	l.Close()

	entries := l.Query(ReceiverLedgerSource, 5000)
	if len(entries) != 3 {
		t.Fatalf("receiver ledger entries = %d, want 3", len(entries))
	}

	// Metrics must agree with the ledger.
	s.NoAuth = true
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest("GET", "/api/metrics.json", nil))
	var metrics MetricsPayload
	if err := json.Unmarshal(rr.Body.Bytes(), &metrics); err != nil {
		t.Fatalf("metrics: %v", err)
	}
	if metrics.Captures != len(entries) {
		t.Fatalf("metrics captures %d != receiver ledger entries %d", metrics.Captures, len(entries))
	}

	// Field-level proof on the verified FT8 clip: decode text, engine
	// label, receiver ID, UTC time and the archive audio link.
	var ft8 *feeds.LedgerEntry
	for i := range entries {
		if strings.Contains(entries[i].ID, "7074") {
			ft8 = &entries[i]
		}
	}
	if ft8 == nil {
		t.Fatalf("no ledger entry for the FT8 capture: %+v", entries)
	}
	if ft8.Source != ReceiverLedgerSource || ft8.DecodedBy != feeds.DecodedByUs {
		t.Fatalf("source/engine = %q/%q", ft8.Source, ft8.DecodedBy)
	}
	if ft8.Detail != "CQ DL1ABC JO31" {
		t.Fatalf("decode text = %q", ft8.Detail)
	}
	if ft8.Receiver != "kiwi.test:8073" {
		t.Fatalf("receiver ID = %q", ft8.Receiver)
	}
	if !ft8.ProviderT.Equal(started) {
		t.Fatalf("UTC = %v, want %v", ft8.ProviderT, started)
	}
	if !strings.HasPrefix(ft8.Ref, "/audio/") || !strings.HasSuffix(ft8.Ref, ".wav") {
		t.Fatalf("audio link = %q", ft8.Ref)
	}
	if ft8.Evidence != feeds.EvidenceVerified || ft8.Gate != "" {
		t.Fatalf("verified clip evidence/gate = %q/%q", ft8.Evidence, ft8.Gate)
	}

	// The gate-rejected clip must keep its honest verdict in the ledger:
	// unverified, with the gate reason, never silently upgraded.
	var noise *feeds.LedgerEntry
	for i := range entries {
		if strings.Contains(entries[i].ID, "10136") {
			noise = &entries[i]
		}
	}
	if noise == nil {
		t.Fatalf("no ledger entry for the rejected clip: %+v", entries)
	}
	if noise.Evidence != feeds.EvidenceUnverified || noise.Gate != "no decodable content" {
		t.Fatalf("rejected clip evidence/gate = %q/%q", noise.Evidence, noise.Gate)
	}

	// The audio link must resolve against the archive.
	rr = httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest("GET", ft8.Ref, nil))
	if rr.Code != 200 {
		t.Fatalf("audio link %s = %d", ft8.Ref, rr.Code)
	}
}

// A second listing (and a restarted process reading the same archive and
// ledger) must not duplicate entries: the count stays equal to metrics.
func TestReceiverCaptureLedgerIdempotent(t *testing.T) {
	capturesDir := t.TempDir()
	ledgerPath := filepath.Join(t.TempDir(), "sdrstream-ledger.jsonl")
	writeReceiverCapture(t, capturesDir, "DATA_14074kHz_ft8_kiwi_test_8073_20261023T002000Z_1.wav", CaptureMetadata{
		FrequencyHz: 14074000, Mode: "ft8", Receiver: "kiwi.test:8073",
		Detail: "CQ TEST", Kind: "data", Started: time.Now().UTC(), Verified: true,
	})

	s, l := receiverLedgerServer(t, capturesDir, ledgerPath)
	_ = s.scanCaptures()
	s.syncReceiverLedger(s.listCaptures()) // a second pass right away
	l.Close()
	if n := len(l.Query(ReceiverLedgerSource, 5000)); n != 1 {
		t.Fatalf("entries after two passes = %d, want 1", n)
	}

	// Restart: a fresh server over the same archive and the same ledger
	// file seeds its seen-set from the ledger and adds nothing.
	l2 := feeds.NewLedger(ledgerPath, 0)
	s2 := &Server{capturesDir: capturesDir}
	m2 := feeds.New(10, func(string, ...any) {})
	m2.SetLedger(l2)
	s2.SetFeeds(m2)
	_ = s2.scanCaptures()
	l2.Close()
	if n := len(l2.Query(ReceiverLedgerSource, 5000)); n != 1 {
		t.Fatalf("entries after restart = %d, want 1", n)
	}
}

// The ledger endpoint serves the receiver entries with the audio link.
func TestReceiverCaptureLedgerServedOverAPI(t *testing.T) {
	capturesDir := t.TempDir()
	ledgerPath := filepath.Join(t.TempDir(), "sdrstream-ledger.jsonl")
	writeReceiverCapture(t, capturesDir, "DATA_518kHz_navtex_kiwi_test_8073_20261023T003000Z_1.wav", CaptureMetadata{
		FrequencyHz: 518000, Mode: "navtex", Receiver: "kiwi.test:8073",
		Detail: "ZCZC FA01 NAVAREA WARNING", Kind: "data", Started: time.Now().UTC(), Verified: true,
	})
	s, l := receiverLedgerServer(t, capturesDir, ledgerPath)
	s.NoAuth = true
	_ = s.scanCaptures()
	l.Close()

	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest("GET", "/api/ledger.json?source=receiver", nil))
	if rr.Code != 200 {
		t.Fatalf("ledger.json = %d", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{`"source": "receiver"`, `"decoded_by": "sdrstream"`, `"receiver": "kiwi.test:8073"`, "/audio/", "ZCZC FA01", `"evidence": "verified"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("ledger.json missing %q: %s", want, body)
		}
	}
}

// Real preserved proofs as fixtures: the two NAVTEX 490 kHz decodes
// recovered from the failed expansion run (docs/recovery/navtex-ext-490-4209
// on feat/navtex-ext-490-4209-recovery; the decode text in these sidecars is
// verbatim from proof-490-oostende-20261023T0010Z.txt and
// proof-490-portpatrick-20261023T0020Z.txt). The original WAVs died with the
// expansion agent's sandbox - only the decode text survives, and that is
// exactly what these fixtures are: sidecars with NO matching WAV. The
// capture list and the ledger must keep them visible with an empty audio
// link (the UI labels that "audio unavailable"), never invent a player.
// Neither passed the live gate - one was rejected by the then English-only
// vocabulary, one ran past the window - so they ledger as unverified with
// the gate reason.
func TestReceiverLedgerRealPreservedProofs(t *testing.T) {
	fixtures, err := filepath.Glob(filepath.Join("testdata", "receiverproof", "*.txt"))
	if err != nil || len(fixtures) != 2 {
		t.Fatalf("fixtures: %v (n=%d)", err, len(fixtures))
	}
	capturesDir := t.TempDir()
	ledgerPath := filepath.Join(t.TempDir(), "sdrstream-ledger.jsonl")
	for _, fx := range fixtures {
		data, err := os.ReadFile(fx)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(capturesDir, filepath.Base(fx)), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	s, l := receiverLedgerServer(t, capturesDir, ledgerPath)
	got := s.scanCaptures()
	l.Close()
	if len(got) != 2 {
		t.Fatalf("scanCaptures = %d, want 2 sidecar-only records", len(got))
	}
	for _, c := range got {
		if c.URL != "" || c.Samples != 0 {
			t.Fatalf("text-only proof must not gain an audio link: %+v", c)
		}
	}
	entries := l.Query(ReceiverLedgerSource, 5000)
	if len(entries) != 2 {
		t.Fatalf("receiver ledger entries = %d, want 2", len(entries))
	}
	var oostende, portpatrick *feeds.LedgerEntry
	for i := range entries {
		switch {
		case strings.Contains(entries[i].ID, "oostende"):
			oostende = &entries[i]
		case strings.Contains(entries[i].ID, "portpatrick"):
			portpatrick = &entries[i]
		}
	}
	if oostende == nil || portpatrick == nil {
		t.Fatalf("missing fixture entries: %+v", entries)
	}
	if oostende.Ref != "" || portpatrick.Ref != "" {
		t.Fatalf("text-only proofs must ledger without an audio link: %q / %q", oostende.Ref, portpatrick.Ref)
	}
	if !strings.Contains(oostende.Detail, "OOSTENDERADIO MSI 395/26") {
		t.Fatalf("oostende decode text = %q", oostende.Detail)
	}
	if oostende.Evidence != feeds.EvidenceUnverified || !strings.Contains(oostende.Gate, "english-only live gate") {
		t.Fatalf("oostende evidence/gate = %q/%q", oostende.Evidence, oostende.Gate)
	}
	if !strings.Contains(portpatrick.Detail, "MET OFFICE") {
		t.Fatalf("portpatrick decode text = %q", portpatrick.Detail)
	}
	if portpatrick.Evidence != feeds.EvidenceUnverified || !strings.Contains(portpatrick.Gate, "gate-incomplete") {
		t.Fatalf("portpatrick evidence/gate = %q/%q", portpatrick.Evidence, portpatrick.Gate)
	}
}
