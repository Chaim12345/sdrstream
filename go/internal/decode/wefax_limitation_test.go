package decode

import (
	"bytes"
	"image/png"
	"math"
	"testing"
)

// Known-limitation characterizations for adversarial synthetic content.
// These tests pin CURRENT decoder behavior that is known to be wrong so
// a behavior change trips loudly instead of slipping by. Each failure
// message says what a fix looks like; they are not correctness gates.

func TestWEFAXFlatGradientPlateauBiasLimitation(t *testing.T) {
	// LIMITATION: flat plateau content gives the clock tracker no
	// per-line structure and biases its local votes (see the mid-chart
	// comment in wefax.go); the render loses alignment down-chart and
	// the band order breaks. Measured on this fixture (2026-10-23):
	// top-of-chart means monotone, bottom-of-chart means
	// [15 37 198 243 251 28] - order destroyed; the fitted drift is
	// ~1184 ppm against a true 0 and the deskew metric reads a
	// vacuous 0.98 on flat content. The geometry-quality tier of the
	// verify.WEFAXImage gate rejects this render via the tracker
	// wander span (~2961 ppm against the 1500 floor), so it cannot
	// be published as verified even though the wedge is real.
	// A tracker fix makes the bottom means monotone and the drift
	// honest; when that happens, convert this into a correctness
	// assertion instead of a limitation pin.
	f := wefaxFixture{rate: 12000, phasingLines: 60, imageLines: 80, startToneSec: 3, flatGradient: true, noiseAmp: 0.15}
	pngData, _ := decodeWEFAXFixture(t, f)
	img, err := png.Decode(bytes.NewReader(pngData))
	if err != nil {
		t.Fatal(err)
	}
	top := wefaxBandMeans(img, 6, 70, 90)
	for b := 1; b < 6; b++ {
		if top[b] < top[b-1]+3 {
			t.Errorf("top-of-chart band %d mean %.0f not above band %d %.0f; fixture assumptions shifted", b, top[b], b-1, top[b-1])
		}
	}
	bottom := wefaxBandMeans(img, 6, 110, 130)
	monotone := true
	for b := 1; b < 6; b++ {
		if bottom[b] < bottom[b-1] {
			monotone = false
		}
	}
	if monotone {
		t.Error("bottom-of-chart band order now holds: plateau-bias limitation appears FIXED - convert this pin into a correctness assertion")
	} else {
		t.Logf("limitation stands: top means %.0f, bottom means %.0f (order lost down-chart)", top, bottom)
	}
}

func TestWEFAXMidChartFalseWedgeFixed(t *testing.T) {
	// Fixed 2026-10-23: mid-chart content with a tall light vertical
	// band used to false-positive the phasing-wedge finder (confident
	// sync, drift ~2981 ppm vs true 800). The finder now requires a
	// black rest-of-line, so this content falls back to line
	// correlation. REMAINING LIMITATION: the wrap period fit is still
	// biased on this gray-bar content (drift ~2596 ppm vs true 800,
	// period score ~0.31), so the decode succeeds with wrong geometry;
	// the verify.WEFAXImage gate rejects it via the period-score floor
	// (0.35), which is what keeps it out of the verified gallery. If a
	// better period estimator lands, the score rises and this test
	// says to convert to a correctness assertion.
	f := wefaxFixture{rate: 12000, imageLines: 120, bars: true, barGradient: true, gradBands: 4, lineSec: 0.5004, noiseAmp: 0.3}
	_, rep := decodeWEFAXFixture(t, f)
	if rep.SyncSource == "phasing-wedge" {
		t.Error("false phasing-wedge sync is back: the rest-of-line blackness gate regressed")
	}
	if rep.SyncSource != "line-correlation" {
		t.Errorf("sync source %q, want line-correlation fallback", rep.SyncSource)
	}
	if rep.PeriodScore >= 0.35 {
		t.Errorf("period score %.2f now clears the verification floor: if drift is also near 800 the period fit improved - convert to a correctness assertion and re-derive the gate floor", rep.PeriodScore)
	}
	if math.Abs(rep.DriftPPM-800) < 500 {
		t.Logf("period fit now near the true 800 ppm (%.0f): bias fixed, tighten this test", rep.DriftPPM)
	} else {
		t.Logf("remaining limitation stands: line-correlation fallback, drift %.0f ppm vs true 800, period score %.2f (gate rejects)", rep.DriftPPM, rep.PeriodScore)
	}
}
