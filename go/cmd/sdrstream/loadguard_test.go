package main

import (
	"testing"
	"time"
)

func guardForTest() *loadGuard {
	return newLoadGuard(30*time.Second, 60*time.Second, 60*time.Second, .85, .65, 512<<20, 400000, 256000)
}

func TestLoadGuardRollingMeanTrimsWindow(t *testing.T) {
	g := guardForTest()
	base := time.Now()
	g.observe(base.Add(-45*time.Second), 3.0) // outside the 30s window
	g.observe(base.Add(-10*time.Second), .4)
	if mean := g.observe(base, .6); mean != .5 {
		t.Fatalf("rolling mean = %v, want .5 (stale sample must be trimmed)", mean)
	}
	if len(g.samples) != 2 {
		t.Fatalf("kept %d samples, want 2 inside the window", len(g.samples))
	}
}

// The auto step-down: a spike, even a long one under 60s, must not shed a
// step; sustained overload past the drop duration sheds exactly one.
func TestLoadGuardAutoStepDown(t *testing.T) {
	g := guardForTest()
	base := time.Now()
	for i := 0; i < 6; i++ {
		at := base.Add(time.Duration(i) * 9 * time.Second) // 0..45s above .85
		g.observe(at, .95)
		if got := g.adjust(at, 2, 3, 0, 0, 0); got != 2 {
			t.Fatalf("dropped before sustained overload: stage %d at %ds", got, i*9)
		}
	}
	at := base.Add(61 * time.Second)
	g.observe(at, .95)
	if got := g.adjust(at, 2, 3, 0, 0, 0); got != 1 {
		t.Fatalf("no auto step-down after 60s above the drop ratio: %d", got)
	}
	// The next drop needs a fresh sustained period, not leftover overload.
	at = base.Add(70 * time.Second)
	g.observe(at, .95)
	if got := g.adjust(at, 1, 3, 0, 0, 0); got != 1 {
		t.Fatalf("second drop without a new sustained period: %d", got)
	}
	if g.held != 1 {
		t.Fatalf("guard holds %d steps, want 1", g.held)
	}
}

func TestLoadGuardRestoresAfterSustainedCalm(t *testing.T) {
	g := guardForTest()
	base := time.Now()
	g.observe(base, .95)
	g.observe(base.Add(61*time.Second), .95)
	if got := g.adjust(base.Add(61*time.Second), 2, 3, 0, 0, 0); got != 1 {
		t.Fatalf("setup drop failed: %d", got)
	}
	// Calm, but not long enough: no restore.
	g.observe(base.Add(100*time.Second), .5)
	g.observe(base.Add(130*time.Second), .5)
	if got := g.adjust(base.Add(130*time.Second), 1, 3, 0, 0, 0); got != 1 {
		t.Fatalf("restored before sustained calm: %d", got)
	}
	// 61s below the restore ratio: the held step comes back.
	g.observe(base.Add(161*time.Second), .5)
	if got := g.adjust(base.Add(161*time.Second), 1, 3, 0, 0, 0); got != 2 {
		t.Fatalf("no restore after 60s below the restore ratio: %d", got)
	}
	if g.held != 0 {
		t.Fatalf("guard still holds %d steps after restore", g.held)
	}
}

func TestLoadGuardHardCapForcesImmediateDrop(t *testing.T) {
	g := guardForTest()
	base := time.Now()
	g.observe(base, .1) // calm: hysteresis alone would never drop here
	if got := g.adjust(base, 2, 3, 512<<20, 0, 0); got != 1 {
		t.Fatalf("RSS hard cap did not force an immediate drop: %d", got)
	}
	if got := g.adjust(base, 2, 3, 0, 400000, 0); got != 1 {
		t.Fatalf("FD hard cap did not force an immediate drop: %d", got)
	}
	if got := g.adjust(base, 2, 3, 0, 0, 256000); got != 1 {
		t.Fatalf("goroutine hard cap did not force an immediate drop: %d", got)
	}
	if got := g.adjust(base, 2, 3, 0, 399999, 255999); got != 2 {
		t.Fatalf("dropped while under every hard cap: %d", got)
	}
	if got := g.adjust(base, 0, 3, 512<<20, 0, 0); got != 0 {
		t.Fatalf("hard cap pushed the stage below zero: %d", got)
	}
}

func TestLoadGuardDefaults(t *testing.T) {
	g := newLoadGuard(0, 0, 0, 0, 0, 0, 0, 0)
	if g.window != 30*time.Second || g.dropFor != 60*time.Second || g.restoreFor != 60*time.Second {
		t.Fatalf("unexpected default timings: %+v", g)
	}
	if g.dropRatio != .85 || g.restoreRatio != .65 {
		t.Fatalf("unexpected default ratios: %+v", g)
	}
	if g.maxRSSBytes != guardMaxRSSBytes || g.maxFDs != guardMaxFDs || g.maxGoroutines != guardMaxGoroutines {
		t.Fatalf("unexpected default caps: %+v", g)
	}
}
