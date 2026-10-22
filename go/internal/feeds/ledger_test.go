package feeds

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLedgerRecordsFirstSightingWithProof(t *testing.T) {
	p := filepath.Join(t.TempDir(), "l.jsonl")
	l := NewLedger(p, 0)
	m := New(100, func(string, ...any) {})
	m.SetLedger(l)
	pt := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	m.Add(Item{ID: "k1", Source: "wspr.live", Kind: "digimode", Label: "K1ABC", Time: pt, Ref: "https://wspr.live/"})
	m.Add(Item{ID: "k1", Source: "wspr.live", Kind: "digimode", Label: "K1ABC", Time: pt}) // refresh
	l.Close()
	got := l.Query("", 10)
	if len(got) != 1 {
		t.Fatalf("entries = %d, want 1", len(got))
	}
	e := got[0]
	if e.DecodedBy != DecodedByProvider || !e.ProviderT.Equal(pt) || e.FetchedAt.IsZero() || e.Ref == "" {
		t.Fatalf("incomplete proof: %+v", e)
	}
}

func TestLedgerRotatesAndQueryFilters(t *testing.T) {
	p := filepath.Join(t.TempDir(), "l.jsonl")
	l := NewLedger(p, 600)
	for i := 0; i < 20; i++ {
		src := "a"
		if i%2 == 1 {
			src = "b"
		}
		l.Append(LedgerEntry{Source: src, ID: string(rune('A' + i)), Label: "x", ProviderT: time.Now(), FetchedAt: time.Now()})
	}
	l.Close()
	if _, err := os.Stat(p + ".1"); err != nil {
		t.Fatalf("no rotation: %v", err)
	}
	for _, e := range l.Query("b", 100) {
		if e.Source != "b" {
			t.Fatalf("filter leaked %q", e.Source)
		}
	}
	all := l.Query("", 3)
	if len(all) != 3 || all[0].ID != string(rune('A'+19)) {
		t.Fatalf("newest-first broken: %+v", all)
	}
}

func TestLedgerSkipsTornLine(t *testing.T) {
	p := filepath.Join(t.TempDir(), "l.jsonl")
	_ = os.WriteFile(p, []byte(`{"source":"a","id":"1","label":"x"}`+"\n"+`{"source":"a","id":"2","la`), 0o600)
	l := &Ledger{path: p}
	if got := l.Query("", 10); len(got) != 1 {
		t.Fatalf("got %d, want 1", len(got))
	}
}
