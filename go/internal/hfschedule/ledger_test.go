package hfschedule

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/decode"
)

func TestRunOnceAppendsProofLedger(t *testing.T) {
	now := utc("2026-10-22T21:00:00Z")
	dir := t.TempDir()
	r := Runner{Dir: dir, Receivers: []Receiver{{URL: "rx1", Type: "websdr", MaxKHz: 30000}}, Now: func() time.Time { return now }, Record: okRecord(720000)}
	a, err := r.RunOnce(context.Background(), []Entry{daylongEntry("svc1")})
	if err != nil || a == nil || a.Status != "recorded" {
		t.Fatalf("audit %+v err %v", a, err)
	}
	b, err := os.ReadFile(filepath.Join(dir, LedgerName))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 1 {
		t.Fatalf("want 1 ledger line, got %d", len(lines))
	}
	var e LedgerEntry
	if err := json.Unmarshal([]byte(lines[0]), &e); err != nil {
		t.Fatal(err)
	}
	if e.Receiver != "rx1" || e.ReceiverType != "websdr" || e.Status != "recorded" || e.UTCStarted != "2026-10-22T21:00:00Z" || e.Name != "svc1" || e.WAV == "" {
		t.Fatalf("bad ledger entry: %+v", e)
	}

	// A failed attempt is also proof: status failed, receiver named, reason kept.
	r.Record = func(context.Context, Receiver, decode.HFTarget, time.Duration) (decode.HFResult, error) {
		return decode.HFResult{AudioWAV: decode.WriteWAV16(make([]int16, 100), 12000)}, nil
	}
	r.Now = func() time.Time { return now.Add(2 * time.Minute) }
	a, err = r.RunOnce(context.Background(), []Entry{daylongEntry("svc2")})
	if err != nil || a == nil {
		t.Fatalf("audit %+v err %v", a, err)
	}
	b, _ = os.ReadFile(filepath.Join(dir, LedgerName))
	lines = strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 ledger lines, got %d", len(lines))
	}
	var e2 LedgerEntry
	if err := json.Unmarshal([]byte(lines[1]), &e2); err != nil {
		t.Fatal(err)
	}
	if e2.Status != "failed" || e2.Error == "" || e2.Receiver != "rx1" {
		t.Fatalf("bad failure ledger entry: %+v", e2)
	}
}

func TestNOAAValidity(t *testing.T) {
	valid, updated := NOAAValidity(strings.NewReader("Valid 19 September 2018\nUpdated 7 August 2018\n\nBOSTON, MASSACHUSETTS, U.S.A.\n"))
	if valid != "19 September 2018" || updated != "7 August 2018" {
		t.Fatalf("valid %q updated %q", valid, updated)
	}
	valid, updated = NOAAValidity(strings.NewReader("no header here\n"))
	if valid != "" || updated != "" {
		t.Fatalf("expected empty, got %q %q", valid, updated)
	}
}
