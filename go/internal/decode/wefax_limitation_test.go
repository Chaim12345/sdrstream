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
	// [15 40 215 61 250 7] - order destroyed. A tracker fix makes the
	// bottom means monotone too; when that happens, convert this into
	// a correctness assertion instead of a limitation pin.
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

func TestWEFAXMidChartFalseWedgeLimitation(t *testing.T) {
	// LIMITATION: mid-chart content with a tall light vertical band
	// false-positives the phasing-wedge finder: the decoder reports a
	// confident phasing-wedge sync with a wildly wrong drift instead of
	// falling back to line correlation. Measured on this fixture
	// (2026-10-23): sync "phasing-wedge", drift ~2981 ppm against a
	// true 800 ppm, deskew corr 0.28. The verify.Image quality gate is
	// geometry-blind (PNG validity, size, variance only), so such a
	// render still passes as verified. A fix rejects the false wedge or
	// fails the decode; when SyncSource stops being "phasing-wedge"
	// here, convert this pin into a correctness assertion.
	f := wefaxFixture{rate: 12000, imageLines: 120, bars: true, barGradient: true, gradBands: 4, lineSec: 0.5004, noiseAmp: 0.3}
	_, rep := decodeWEFAXFixture(t, f)
	if rep.SyncSource != "phasing-wedge" {
		t.Errorf("sync source %q: false-wedge limitation no longer reproduces - if the decoder now rejects the wedge, convert this pin into a correctness assertion", rep.SyncSource)
	}
	if math.Abs(rep.DriftPPM-800) < 500 {
		t.Errorf("fitted drift %.0f ppm now near the true 800 ppm: limitation appears FIXED - convert this pin into a correctness assertion", rep.DriftPPM)
	} else {
		t.Logf("limitation stands: false phasing-wedge sync, drift %.0f ppm vs true 800", rep.DriftPPM)
	}
}
