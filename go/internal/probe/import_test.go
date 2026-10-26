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
