package probe

import (
	"os"
	"path/filepath"
	"testing"
)

func TestImportLedgerVerbatimVerdicts(t *testing.T) {
	ledger := `{"family":"KiwiSDR","endpoint":"a.example:8073","started_at":"2026-10-26T09:00:00Z","dial_ok":true,"outcome":"iq-samples-decoded"}
{"family":"KiwiSDR","endpoint":"b.example:8073","started_at":"2026-10-26T09:01:00Z","dial_ok":true,"outcome":"iq-mode-stream-received-format-undecoded"}
{"family":"OpenWebRX","endpoint":"c.example:8073","started_at":"2026-10-26T09:02:00Z","dial_ok":true,"outcome":"audio-frames-received"}
{"family":"OpenWebRX","endpoint":"d.example:8073","started_at":"2026-10-26T09:03:00Z","dial_ok":false,"outcome":"dns-fail"}
`
	p := filepath.Join(t.TempDir(), "ledger.jsonl")
	if err := os.WriteFile(p, []byte(ledger), 0o600); err != nil {
		t.Fatal(err)
	}
	c := NewCatalog()
	n, err := c.ImportLedger(p)
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Fatalf("imported %d, want 4", n)
	}
	a, _ := c.Get("a.example:8073")
	if !a.Reachable || a.Verdict != "iq-samples-decoded" || a.Kind != "kiwi" {
		t.Fatalf("a: %+v", a)
	}
	b, _ := c.Get("b.example:8073")
	if !b.Reachable || b.Verdict != "iq-mode-stream-received-format-undecoded" {
		t.Fatalf("b verdict must stay verbatim: %+v", b)
	}
	d, _ := c.Get("d.example:8073")
	if d.Reachable || d.Verdict != "dns-fail" {
		t.Fatalf("d: %+v", d)
	}
	if a.CheckedAt.Format("2006-01-02") != "2026-10-26" {
		t.Fatalf("checked_at lost: %v", a.CheckedAt)
	}
}

func TestImportLedgerRejectsMalformed(t *testing.T) {
	p := filepath.Join(t.TempDir(), "bad.jsonl")
	if err := os.WriteFile(p, []byte("{\"family\":\"KiwiSDR\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewCatalog().ImportLedger(p); err == nil {
		t.Fatal("row without endpoint imported")
	}
}

func TestImportKindNewFamilies(t *testing.T) {
	for in, want := range map[string]string{"UberSDR": "ubersdr", "Web-888": "web-888", "web888": "web-888"} {
		if got := importKind(in); got != want {
			t.Fatalf("importKind(%q)=%q want %q", in, got, want)
		}
	}
}

func TestRecordURLPrefersCandidateScheme(t *testing.T) {
	if got := recordURL(LedgerRow{Endpoint: "x.example:443", URL: "https://x.example"}); got != "https://x.example" {
		t.Fatalf("got %q", got)
	}
	if got := recordURL(LedgerRow{Endpoint: "x.example:8073"}); got != "http://x.example:8073" {
		t.Fatalf("got %q", got)
	}
}

// Re-importing the same ledger (every server restart re-imports) must be
// idempotent: the historical row is one observation, so the failure counter
// must stay 1 and no reachability-transition events may appear. The old
// path routed imports through Update(), which incremented the counter each
// time and fabricated "N consecutive failures".
func TestImportLedgerIdempotentNoFakeHistory(t *testing.T) {
	ledger := `{"family":"Web-888","endpoint":"w.example:8073","started_at":"2026-10-26T09:00:00Z","dial_ok":false,"outcome":"skipped-status-unreadable"}
`
	p := filepath.Join(t.TempDir(), "ledger.jsonl")
	if err := os.WriteFile(p, []byte(ledger), 0o600); err != nil {
		t.Fatal(err)
	}
	c := NewCatalog()
	for i := 0; i < 5; i++ {
		if _, err := c.ImportLedger(p); err != nil {
			t.Fatal(err)
		}
	}
	w, _ := c.Get("w.example:8073")
	if w.ConsecFailures != 1 {
		t.Fatalf("after 5 imports ConsecFailures = %d, want 1 (one observation)", w.ConsecFailures)
	}
	if n := len(c.Snapshot().Events); n != 0 {
		t.Fatalf("imports emitted %d transition events, want 0", n)
	}
	// A later live probe failure increments from the imported baseline.
	c.Update(Record{Key: "w.example:8073", Kind: "web-888", Reachable: false, Verdict: "ws-fail"})
	w, _ = c.Get("w.example:8073")
	if w.ConsecFailures != 2 {
		t.Fatalf("after live probe failure ConsecFailures = %d, want 2", w.ConsecFailures)
	}
}
