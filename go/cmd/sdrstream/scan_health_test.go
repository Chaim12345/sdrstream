package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
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
	p.table.ObserveCapture(stationHostPortKey("http://a.example:8073/"), true)
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

func TestStationHostPortKeyNormalisesForms(t *testing.T) {
	// One receiver, one key - no matter which roster form or call site
	// named it. QA suspected observe* and the beacon loop keyed records
	// differently ("", host) vs ("KiwiSDR", host); the kind argument was in
	// fact ignored, and it is now gone from the signature so the key
	// cannot diverge.
	cases := map[string]string{
		"http://Example.COM:8073/":   "example.com:8073",
		"example.com:8073":           "example.com:8073",
		"Example.COM:8073/":          "example.com:8073",
		"http://example.com":         "example.com:80",
		"https://example.com/":       "example.com:443",
		"http://example.com:8073/x?": "example.com:8073",
	}
	for raw, want := range cases {
		if got := stationHostPortKey(raw); got != want {
			t.Errorf("stationHostPortKey(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestPickBeaconCandidatesPrefersIdleThenPickedIdle(t *testing.T) {
	p := testPool(t, 3)
	p.mu.Lock()
	p.picks = map[string]bool{"b.example:8073": true}
	p.mu.Unlock()
	entries := []string{
		"KiwiSDR|a.example:8073",
		"KiwiSDR|b.example:8073",
		"WebSDR|http://c.example:8901/", // wrong kind: never a beacon candidate
	}
	got := p.pickBeaconCandidates(entries, map[string]time.Time{}, time.Now()).candidates
	if len(got) != 2 {
		t.Fatalf("candidates = %v, want a (idle) and b (picked, no visit)", got)
	}
	if got[0].host != "a.example:8073" || got[1].host != "b.example:8073" {
		t.Fatalf("order = %v, want idle candidate before picked-but-idle", got)
	}
	if got[0].key != "a.example:8073" || got[1].key != "b.example:8073" {
		t.Fatalf("keys = %v, want normalised host:port", got)
	}
}

func TestPickBeaconCandidatesCoversPickedOnlyRoster(t *testing.T) {
	// Regression for QA's 15-minute silence: when the pool covers every
	// KiwiSDR roster entry, skipping picks wholesale left
	// beacon_measured_at zero forever. Picked members not in a visit must
	// still be measured.
	p := testPool(t, 3)
	p.mu.Lock()
	p.picks = map[string]bool{"a.example:8073": true, "b.example:8073": true}
	p.mu.Unlock()
	entries := []string{"KiwiSDR|a.example:8073", "KiwiSDR|b.example:8073"}
	got := p.pickBeaconCandidates(entries, map[string]time.Time{}, time.Now()).candidates
	if len(got) != 2 {
		t.Fatalf("picked-only roster must still be measurable, got %v", got)
	}
	// A pool member in a visit is left alone.
	p.mu.Lock()
	p.visits["a.example:8073"] = true
	p.mu.Unlock()
	plan := p.pickBeaconCandidates(entries, map[string]time.Time{}, time.Now())
	got = plan.candidates
	if len(got) != 1 || got[0].host != "b.example:8073" {
		t.Fatalf("in-visit member must not be a candidate, got %v", got)
	}
	if len(plan.inVisit) != 1 || plan.inVisit[0].host != "a.example:8073" {
		t.Fatalf("in-visit member must be reported for reservation, got %v", plan.inVisit)
	}
	// A fresh reading makes a host due-never for beaconSNRStale.
	fresh := map[string]time.Time{"b.example:8073": time.Now()}
	p.mu.Lock()
	delete(p.visits, "a.example:8073")
	p.mu.Unlock()
	plan = p.pickBeaconCandidates(entries, fresh, time.Now())
	if got = plan.candidates; len(got) != 1 || got[0].host != "a.example:8073" {
		t.Fatalf("fresh reading must be skipped, got %v", got)
	}
	if plan.fresh != 1 {
		t.Fatalf("fresh count = %d, want 1", plan.fresh)
	}
}

func TestObserveHooksTrackVisits(t *testing.T) {
	p := testPool(t, 1)
	p.observeProbe("http://a.example:8073/", kiwisdr.Status{})
	p.mu.Lock()
	marked := p.visits["a.example:8073"]
	p.mu.Unlock()
	if !marked {
		t.Fatal("observeProbe must mark the visit in flight")
	}
	// Even a skipped visit (not a table observation) clears the mark.
	p.observeOutcome("http://a.example:8073/", scanOutcome{skipped: true})
	p.mu.Lock()
	marked = p.visits["a.example:8073"]
	p.mu.Unlock()
	if marked {
		t.Fatal("observeOutcome must clear the visit mark")
	}
}

// stubBeaconNetwork swaps the prober's network calls for fakes.
func stubBeaconNetwork(t *testing.T, preflight func(host string) kiwisdr.Status, measure func(host string) (float64, error)) {
	t.Helper()
	oldPre, oldMeas := beaconPreflight, beaconMeasure
	t.Cleanup(func() { beaconPreflight, beaconMeasure = oldPre, oldMeas })
	beaconPreflight = func(_ context.Context, host string) kiwisdr.Status { return preflight(host) }
	beaconMeasure = func(_ context.Context, host string, _ kiwisdr.BeaconTarget, _ time.Duration, _ kiwisdr.Options) (float64, error) {
		return measure(host)
	}
}

func TestProbePassReservesMidVisitHostThenMeasures(t *testing.T) {
	p := testPool(t, 1)
	var logs []string
	p.logf = func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }
	var measured []string
	stubBeaconNetwork(t,
		func(host string) kiwisdr.Status {
			return kiwisdr.Status{Verdict: kiwisdr.VerdictOK, Latency: 100 * time.Millisecond}
		},
		func(host string) (float64, error) { measured = append(measured, host); return 17.5, nil })
	roster := newScanRoster(nil)
	roster.AddManual("KiwiSDR|a.example:8073")
	// The pool covers the whole roster and the one host is mid-visit: QA's
	// steady state, in which no measurement ever landed.
	p.mu.Lock()
	p.picks = map[string]bool{"a.example:8073": true}
	p.visits["a.example:8073"] = true
	p.mu.Unlock()
	if retry := p.probeOneIdleBeacon(context.Background(), roster); !retry {
		t.Fatal("a due mid-visit host must schedule a retry")
	}
	if len(measured) != 0 {
		t.Fatal("must never measure during a visit")
	}
	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "reserved a.example:8073") {
		t.Fatalf("pass must log the reservation, got:\n%s", joined)
	}
	// While reserved, the dispatcher refuses to start a new visit.
	if p.tryStartVisit("a.example:8073") {
		t.Fatal("reserved host must not be dispatched")
	}
	// The visit ends; the next pass measures in the quiet window.
	p.mu.Lock()
	delete(p.visits, "a.example:8073")
	p.mu.Unlock()
	if retry := p.probeOneIdleBeacon(context.Background(), roster); retry {
		t.Fatal("the measurement should complete without another retry")
	}
	if len(measured) != 1 || measured[0] != "a.example:8073" {
		t.Fatalf("measured = %v, want the reserved host", measured)
	}
	rec := p.table.Snapshot()[0]
	if rec.BeaconMeasuredAt.IsZero() || rec.BeaconSNRdB != 17.5 {
		t.Fatalf("beacon reading did not land: %+v", rec)
	}
	// The reservation is released so scanning resumes.
	if !p.tryStartVisit("a.example:8073") {
		t.Fatal("reservation must be released after the measurement")
	}
}

func TestProbePassLogsFreshAndEmptyRoster(t *testing.T) {
	p := testPool(t, 3)
	var logs []string
	p.logf = func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }
	p.table.ObserveBeaconSNR("a.example:8073", "WWV/BPM/RWM 10 MHz", 20)
	roster := newScanRoster(nil)
	roster.AddManual("KiwiSDR|a.example:8073")
	if retry := p.probeOneIdleBeacon(context.Background(), roster); retry {
		t.Fatal("a fresh roster needs no retry")
	}
	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "beacon probe pass: 1 KiwiSDR roster entries: 1 fresh, none due") {
		t.Fatalf("pass summary missing, got:\n%s", joined)
	}
	logs = nil
	p.probeOneIdleBeacon(context.Background(), nil)
	if !strings.Contains(strings.Join(logs, "\n"), "no roster") {
		t.Fatalf("nil roster must be logged, got %v", logs)
	}
}

func TestProbePassPreflightSkipIsLoggedAndNextTried(t *testing.T) {
	p := testPool(t, 3)
	var logs []string
	p.logf = func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }
	var measured []string
	stubBeaconNetwork(t,
		func(host string) kiwisdr.Status {
			if strings.HasPrefix(host, "busy.") {
				return kiwisdr.Status{Verdict: kiwisdr.VerdictBusy, Err: "users 4/4"}
			}
			return kiwisdr.Status{Verdict: kiwisdr.VerdictOK}
		},
		func(host string) (float64, error) { measured = append(measured, host); return 9, nil })
	roster := newScanRoster(nil)
	roster.AddManual("KiwiSDR|busy.example:8073", "KiwiSDR|free.example:8073")
	p.probeOneIdleBeacon(context.Background(), roster)
	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "beacon busy.example:8073 skipped: preflight verdict busy: users 4/4") {
		t.Fatalf("preflight skip must be logged, got:\n%s", joined)
	}
	if len(measured) != 1 || measured[0] != "free.example:8073" {
		t.Fatalf("the free candidate should be measured, got %v", measured)
	}
}

func TestTryStartVisitHonoursReservation(t *testing.T) {
	p := testPool(t, 1)
	if !p.tryStartVisit("http://a.example:8073/") {
		t.Fatal("unreserved host must dispatch")
	}
	p.observeOutcome("a.example:8073", scanOutcome{})
	p.reserveForBeacon("a.example:8073", time.Now())
	if p.tryStartVisit("a.example:8073") {
		t.Fatal("reserved host must not dispatch")
	}
	// An expired reservation no longer blocks.
	p.reserveForBeacon("a.example:8073", time.Now().Add(-2*beaconReserveWindow))
	if !p.tryStartVisit("a.example:8073") {
		t.Fatal("expired reservation must not dispatch-block")
	}
	p.observeOutcome("a.example:8073", scanOutcome{})
	p.reserveForBeacon("a.example:8073", time.Now())
	p.releaseBeacon("a.example:8073")
	if !p.tryStartVisit("a.example:8073") {
		t.Fatal("released reservation must not dispatch-block")
	}
}
