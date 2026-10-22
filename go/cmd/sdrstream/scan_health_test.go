package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/kiwisdr"
)

func testPool(t *testing.T, size int) *healthPool {
	t.Helper()
	p, err := newHealthPool(size, filepath.Join(t.TempDir(), "receiver-health.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPickStationsFiltersRoster(t *testing.T) {
	p := testPool(t, 3)
	roster := []string{
		"KiwiSDR|a.example:8073",
		"KiwiSDR|b.example:8073",
		"WebSDR|http://c.example:8901/",
		"KiwiSDR|d.example:8073",
		"KiwiSDR|e.example:8073",
	}
	got := p.pickStations(roster)
	if len(got) != 3 {
		t.Fatalf("pool must hold 3, got %v", got)
	}
	want := map[string]bool{}
	for _, entry := range got {
		want[entry] = true
	}
	// Rotation: after enough repicks every receiver gets a turn.
	for i := 0; i < 6; i++ {
		p.mu.Lock()
		p.repick = time.Now().Add(-time.Second)
		p.mu.Unlock()
		if !p.repickDue() {
			t.Fatal("repick not due after forcing the clock")
		}
		for _, entry := range p.pickStations(roster) {
			want[entry] = true
		}
	}
	for _, entry := range roster {
		if !want[entry] {
			t.Fatalf("%s never entered the pool", entry)
		}
	}
	// Every pick is a roster member, never a stranger.
	for _, entry := range p.pickStations(roster) {
		found := false
		for _, r := range roster {
			if r == entry {
				found = true
			}
		}
		if !found {
			t.Fatalf("pool returned %s, not on the roster", entry)
		}
	}
}

func TestPickStationsPrefersHealthy(t *testing.T) {
	p := testPool(t, 1)
	p.table.ObserveProbe("a.example:8073", 200*time.Millisecond, true)
	p.table.ObserveBeaconSNR("a.example:8073", "WWV/BPM/RWM 10 MHz", 25)
	p.table.ObserveCapture("a.example:8073", true)
	got := p.pickStations([]string{"KiwiSDR|a.example:8073", "KiwiSDR|b.example:8073"})
	if len(got) != 1 || got[0] != "KiwiSDR|a.example:8073" {
		t.Fatalf("healthiest receiver must win a pool of one, got %v", got)
	}
}

func TestObserveOutcomeClassification(t *testing.T) {
	p := testPool(t, 3)
	// Success: last successful capture set, streak cleared.
	p.observeOutcome("a.example:8073", scanOutcome{})
	snap := p.table.Snapshot()
	if len(snap) != 1 || snap[0].Captures != 1 || snap[0].LastCapture.IsZero() {
		t.Fatalf("clean visit should count as a successful capture: %+v", snap)
	}
	// Receiver refusal: failure.
	p.observeOutcome("a.example:8073", scanOutcome{refused: true})
	// Timeout: failure.
	p.observeOutcome("a.example:8073", scanOutcome{timedOut: true})
	// Skipped (backoff/budget/own deadline): no observation.
	p.observeOutcome("a.example:8073", scanOutcome{skipped: true})
	// Local resource pressure: not the receiver's fault, no observation.
	p.observeOutcome("a.example:8073", scanOutcome{refused: true, localResource: true})
	snap = p.table.Snapshot()
	rec := snap[0]
	if rec.CaptureFailures != 2 || rec.ConsecutiveFailures != 2 {
		t.Fatalf("want 2 failures and a streak of 2, got %+v", rec)
	}
	if rec.Captures != 1 {
		t.Fatalf("skips and local failures must not count as captures: %+v", rec)
	}
}

func TestObserveProbeNormalisesHost(t *testing.T) {
	p := testPool(t, 3)
	// The same receiver under URL and host:port forms must be one record.
	p.observeProbe("http://a.example:8073/", kiwisdr.Status{Verdict: kiwisdr.VerdictOK, Latency: 300 * time.Millisecond})
	p.table.ObserveCapture(stationHostPortKey("KiwiSDR", "http://a.example:8073/"), true)
	if n := len(p.table.Snapshot()); n != 1 {
		t.Fatalf("expected one record, got %d", n)
	}
	rec := p.table.Snapshot()[0]
	if rec.Probes != 1 || rec.Captures != 1 || rec.LatencyMS < 299 || rec.LatencyMS > 301 {
		t.Fatalf("probe observation lost: %+v", rec)
	}
}

func TestHealthPoolSaveLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "receiver-health.json")
	p, err := newHealthPool(2, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	p.table.ObserveCapture("a.example:8073", true)
	p.save()
	back, err := newHealthPool(2, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(back.table.Snapshot()); n != 1 {
		t.Fatalf("table did not survive the restart, %d records", n)
	}
}

func TestNewHealthPoolSizeValidation(t *testing.T) {
	if _, err := newHealthPool(0, "", nil); err == nil {
		t.Fatal("size 0 must be rejected")
	}
	if _, err := newHealthPool(12, "", nil); err == nil {
		t.Fatal("the retired 12-connection pool must be rejected")
	}
	if _, err := newHealthPool(3, "", nil); err != nil {
		t.Fatal(err)
	}
}
