package decode

import "testing"

// Counterexample guards for the content-run veto in trackLineClock
// (the veto is a fixture-derived heuristic, so its legitimate-wander
// envelope is pinned by measurement): slow SUSTAINED wander holds one
// sign for half its swing period, and at these amplitudes/periods the
// per-line change exceeds a hop in ~20-line runs - near the veto bar.
// Measured WITH the veto active (2026-10-23): deskew 0.970 / 0.968,
// the swing still tracked. If the veto ever eats legitimate sustained
// wander these fail first.
func TestWEFAXSlowSustainedWander3000x200(t *testing.T) {
	rate := 12000.0
	samples, _ := encodeWEFAXWandering(rate, 300, 300, 3000, 200)
	_, rep, err := WEFAXImageDetailed(samples, int(rate), 120, 576)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	t.Logf("3000ppm/200line: deskew %.3f drift %+.0f span %.0f", rep.DeskewCorr, rep.DriftPPM, rep.DriftMaxPPM-rep.DriftMinPPM)
	if rep.DeskewCorr < 0.55 {
		t.Errorf("deskew %.2f under 0.55", rep.DeskewCorr)
	}
}
func TestWEFAXSlowSustainedWander5000x120(t *testing.T) {
	rate := 12000.0
	samples, _ := encodeWEFAXWandering(rate, 300, 300, 5000, 120)
	_, rep, err := WEFAXImageDetailed(samples, int(rate), 120, 576)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	t.Logf("5000ppm/120line: deskew %.3f drift %+.0f span %.0f", rep.DeskewCorr, rep.DriftPPM, rep.DriftMaxPPM-rep.DriftMinPPM)
	if rep.DeskewCorr < 0.55 {
		t.Errorf("deskew %.2f under 0.55", rep.DeskewCorr)
	}
}
