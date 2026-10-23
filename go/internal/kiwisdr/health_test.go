package kiwisdr

import (
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestHealthScoreOrdering(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	tab := NewHealthTable()
	tab.Now = func() time.Time { return base }

	// good: fast probe, strong beacon, recent capture
	tab.ObserveProbe("good:8073", 300*time.Millisecond, true)
	tab.ObserveBeaconSNR("good:8073", "WWV/BPM/RWM 10 MHz", 25)
	tab.ObserveCapture("good:8073", true)
	// mid: average everything
	tab.ObserveProbe("mid:8073", 2*time.Second, true)
	tab.ObserveCapture("mid:8073", true)
	// bad: slow probe, no beacon, failing captures
	tab.ObserveProbe("bad:8073", 6*time.Second, true)
	tab.ObserveCapture("bad:8073", false)
	tab.ObserveCapture("bad:8073", false)
	tab.ObserveCapture("bad:8073", false)

	sg, sm, sb := tab.Score("good:8073"), tab.Score("mid:8073"), tab.Score("bad:8073")
	if !(sg > sm && sm > sb) {
		t.Fatalf("expected good > mid > bad, got %.3f %.3f %.3f", sg, sm, sb)
	}
	// Consecutive failures halve the score each time: 3 failures = 1/8.
	fresh := NewHealthTable()
	fresh.Now = func() time.Time { return base }
	fresh.ObserveProbe("bad:8073", 6*time.Second, true)
	before := fresh.Score("bad:8073")
	for i := 0; i < 3; i++ {
		fresh.ObserveCapture("bad:8073", false)
	}
	after := fresh.Score("bad:8073")
	// The failures also zero the neutral 0.5 capture sub-score (weight
	// 0.35); the remaining base halves per consecutive failure: /8 for 3.
	want := (before - 0.35*0.5) / 8
	if after < want*0.95 || after > want*1.05 {
		t.Fatalf("expected ~%.4f after 3 failures, before=%.4f after=%.4f", want, before, after)
	}
}

func TestHealthPickRotation(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	tab := NewHealthTable()
	now := base
	tab.Now = func() time.Time { return now }

	hosts := []string{"a:8073", "b:8073", "c:8073", "d:8073", "e:8073"}
	// Equal scores: rotation must cycle through all of them.
	seen := map[string]int{}
	for i := 0; i < 5; i++ {
		picks := tab.Pick(hosts, 3)
		if len(picks) != 3 {
			t.Fatalf("round %d: got %d picks, want 3", i, len(picks))
		}
		for _, h := range picks {
			seen[h]++
		}
		now = now.Add(time.Minute)
	}
	for _, h := range hosts {
		if seen[h] == 0 {
			t.Fatalf("host %s never picked across 5 rounds: %v", h, seen)
		}
	}

	// A much healthier host must always be in the pool.
	tab2 := NewHealthTable()
	tab2.Now = func() time.Time { return now }
	tab2.ObserveProbe("star:8073", 100*time.Millisecond, true)
	tab2.ObserveBeaconSNR("star:8073", "WWV/BPM 5 MHz", 28)
	tab2.ObserveCapture("star:8073", true)
	picks := tab2.Pick(append(hosts, "star:8073"), 3)
	found := false
	for _, h := range picks {
		if h == "star:8073" {
			found = true
		}
	}
	if !found {
		t.Fatalf("healthiest host not picked: %v", picks)
	}
}

func TestHealthPickCooldown(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	tab := NewHealthTable()
	now := base
	tab.Now = func() time.Time { return now }
	tab.ObserveProbe("flaky:8073", 200*time.Millisecond, true)
	tab.ObserveCapture("flaky:8073", false)

	if picks := tab.Pick([]string{"flaky:8073"}, 1); len(picks) != 0 {
		t.Fatalf("host in cooldown must not be picked, got %v", picks)
	}
	now = now.Add(11 * time.Minute) // past the 10-minute base cooldown
	if picks := tab.Pick([]string{"flaky:8073"}, 1); len(picks) != 1 {
		t.Fatalf("host past cooldown should be pickable, got %v", picks)
	}

	// Second failure doubles the cooldown to 20 minutes.
	tab.ObserveCapture("flaky:8073", false)
	now = now.Add(11 * time.Minute)
	if picks := tab.Pick([]string{"flaky:8073"}, 1); len(picks) != 0 {
		t.Fatalf("second failure should still be cooling down, got %v", picks)
	}
	now = now.Add(10 * time.Minute)
	if picks := tab.Pick([]string{"flaky:8073"}, 1); len(picks) != 1 {
		t.Fatalf("host past doubled cooldown should be pickable, got %v", picks)
	}
}

func TestHealthPickClampsPoolSize(t *testing.T) {
	tab := NewHealthTable()
	hosts := []string{"a:8073", "b:8073", "c:8073", "d:8073"}
	if got := len(tab.Pick(hosts, 99)); got != HealthPoolMax {
		t.Fatalf("pool size must clamp to %d, got %d", HealthPoolMax, got)
	}
	if got := len(tab.Pick(hosts, 0)); got != HealthPoolMin {
		t.Fatalf("pool size must clamp to %d, got %d", HealthPoolMin, got)
	}
	if got := tab.Pick(nil, 3); got != nil {
		t.Fatalf("no candidates, no picks; got %v", got)
	}
}

func TestHealthPersistenceRoundTrip(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	tab := NewHealthTable()
	tab.Now = func() time.Time { return base }
	tab.ObserveProbe("a:8073", 400*time.Millisecond, true)
	tab.ObserveProbe("a:8073", 600*time.Millisecond, true)
	tab.ObserveProbe("a:8073", 0, false)
	tab.ObserveBeaconSNR("a:8073", "WWV/BPM/RWM 10 MHz", 18.5)
	tab.ObserveCapture("a:8073", true)
	tab.ObserveCapture("b:8073", false)

	path := filepath.Join(t.TempDir(), "state", "receiver-health.json")
	if err := tab.SaveHealth(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	back, err := LoadHealth(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	back.Now = func() time.Time { return base }
	if tab.Score("a:8073") != back.Score("a:8073") {
		t.Fatalf("score changed across persistence: %.6f vs %.6f", tab.Score("a:8073"), back.Score("a:8073"))
	}
	snap := back.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("expected 2 records, got %d", len(snap))
	}
	var a HealthRecord
	for _, r := range snap {
		if r.Host == "a:8073" {
			a = r
		}
	}
	if a.Probes != 3 || a.ProbeFailures != 1 || a.Captures != 1 {
		t.Fatalf("counters lost: %+v", a)
	}
	if a.BeaconSNRdB != 18.5 || a.BeaconName != "WWV/BPM/RWM 10 MHz" {
		t.Fatalf("beacon lost: %+v", a)
	}
	// EWMA of 400 then 600 ms with alpha 0.4 = 480 ms.
	if a.LatencyMS < 479 || a.LatencyMS > 481 {
		t.Fatalf("EWMA wrong: %.2f", a.LatencyMS)
	}
	if a.LastCapture.IsZero() || !a.LastFailure.IsZero() {
		t.Fatalf("capture timestamps wrong: %+v", a)
	}

	// Missing file loads as an empty table.
	empty, err := LoadHealth(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil || len(empty.Snapshot()) != 0 {
		t.Fatalf("missing file should give an empty table: %v %v", empty, err)
	}
}

func TestHealthConcurrentUse(t *testing.T) {
	tab := NewHealthTable()
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			hosts := []string{"a:8073", "b:8073", "c:8073"}
			for i := 0; i < 200; i++ {
				tab.ObserveProbe(hosts[(w+i)%3], time.Duration(i)*time.Millisecond, i%5 != 0)
				tab.ObserveCapture(hosts[(w+i)%3], i%7 != 0)
				tab.ObserveBeaconSNR(hosts[(w+i)%3], "b", float64(i%30))
				tab.Pick(hosts, 2)
				tab.Snapshot()
			}
		}(w)
	}
	wg.Wait()
}
