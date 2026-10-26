package decode

import (
	"bytes"
	"image/png"
	"math"
	"math/rand"
	"testing"
)

// appendNoise pads the signal with low-level receiver noise, the state
// of a capture that keeps recording after the transmitter drops.
func appendNoise(samples []float64, rate float64, sec float64) []float64 {
	rng := rand.New(rand.NewSource(7))
	n := int(sec * rate)
	for i := 0; i < n; i++ {
		samples = append(samples, 0.05*(rng.Float64()*2-1))
	}
	return samples
}

// Regression: a capture running past the end of the transmission used
// to decode the dead tail too - static rows in the image and junk line
// "votes" that poisoned the drift statistics the verifier gates on (on
// DWD 3855, 2026-10-26: 900 dead-tail lines pinned the min drift at
// -2385 ppm and the mean at -898 ppm while the keyed content tracked
// ~0). The trace must crop at the end of sustained keying.
func TestWEFAXKeyingEndTruncation(t *testing.T) {
	const rate = 8000
	txSec := 3.0 + float64(60+80)*0.5 // start tone + phasing + image
	samples := appendNoise(synthWEFAXExactLines(rate, 60, 80, 3), rate, 30)
	pngData, rep, err := WEFAXImageDetailed(samples, rate, 120, 576)
	if err != nil {
		t.Fatalf("decode failed: %v (report %+v)", err, rep)
	}
	if rep.KeyingEndSec <= 0 {
		t.Errorf("KeyingEndSec = %v, want the end of sustained keying (~%.0f s)", rep.KeyingEndSec, txSec)
	} else if math.Abs(rep.KeyingEndSec-txSec) > 3 {
		t.Errorf("KeyingEndSec = %.1f s, want within 3 s of the transmission end %.0f s", rep.KeyingEndSec, txSec)
	}
	img, err := png.Decode(bytes.NewReader(pngData))
	if err != nil {
		t.Fatal(err)
	}
	// 140 content lines (60 phasing + 80 image); the 30 s tail is 60
	// more lines that must NOT render.
	if h := img.Bounds().Dy(); h > 140+6 {
		t.Errorf("height %d includes the dead tail (want ~140)", h)
	}
	if rep.DriftMaxPPM-rep.DriftMinPPM > 1500 {
		t.Errorf("wander span %.0f ppm over the keyed region, want clean (dead-tail votes leaked into the stats?)", rep.DriftMaxPPM-rep.DriftMinPPM)
	}
	if math.Abs(rep.DriftPPM) > 500 {
		t.Errorf("drift %.0f ppm on clean signal, want ~0", rep.DriftPPM)
	}
}

// Control: keying that reaches the end of the capture is not a dead
// tail - nothing is cropped and the decode is exactly as before.
func TestWEFAXKeyingToTraceEndNoTruncation(t *testing.T) {
	const rate = 8000
	samples := synthWEFAXExactLines(rate, 60, 80, 3)
	pngData, rep, err := WEFAXImageDetailed(samples, rate, 120, 576)
	if err != nil {
		t.Fatalf("decode failed: %v (report %+v)", err, rep)
	}
	if rep.KeyingEndSec != 0 {
		t.Errorf("KeyingEndSec = %.1f on a capture ending with the transmission, want 0 (no crop)", rep.KeyingEndSec)
	}
	img, err := png.Decode(bytes.NewReader(pngData))
	if err != nil {
		t.Fatal(err)
	}
	if h := img.Bounds().Dy(); h < 80+60/2 {
		t.Errorf("height %d, want >= %d (content lost?)", h, 80+60/2)
	}
}

// appendHeldTone pads the signal with a steady white tone at full
// amplitude: a transmitter that stops keying but keeps the carrier on
// (DWD 3855 held the white tone for 7+ minutes after the chart on
// 2026-10-26).
func appendHeldTone(samples []float64, rate float64, sec float64) []float64 {
	rng := rand.New(rand.NewSource(11))
	n := int(sec * rate)
	phase := 0.0
	for i := 0; i < n; i++ {
		phase += 2 * math.Pi * 2300.0 / rate
		samples = append(samples, 0.6*math.Sin(phase)+0.05*(rng.Float64()*2-1))
	}
	return samples
}

// A held white tone after the chart is not chart content: the render
// must stop at the end of keying, not render the tone as white rows.
func TestWEFAXHeldToneTailTruncation(t *testing.T) {
	const rate = 8000
	txSec := 3.0 + float64(60+80)*0.5
	samples := appendHeldTone(synthWEFAXExactLines(rate, 60, 80, 3), rate, 30)
	pngData, rep, err := WEFAXImageDetailed(samples, rate, 120, 576)
	if err != nil {
		t.Fatalf("decode failed: %v (report %+v)", err, rep)
	}
	if rep.KeyingEndSec <= 0 || math.Abs(rep.KeyingEndSec-txSec) > 3 {
		t.Errorf("KeyingEndSec = %.1f s, want within 3 s of the keying end %.0f s", rep.KeyingEndSec, txSec)
	}
	img, err := png.Decode(bytes.NewReader(pngData))
	if err != nil {
		t.Fatal(err)
	}
	if h := img.Bounds().Dy(); h > 140+6 {
		t.Errorf("height %d includes the held-tone tail (want ~140)", h)
	}
}

// insertGap splices sec seconds of receiver noise into the signal at
// the given line boundary (line count after the start tone), modelling
// a mid-chart fade with keying resuming afterwards.
func insertGap(samples []float64, rate float64, lineSec float64, startToneSec float64, atLine int, sec float64) []float64 {
	lineSamplesF := lineSec * rate
	cut := int(math.Round(startToneSec*rate)) + int(math.Round(float64(atLine)*lineSamplesF))
	gap := appendNoise(nil, rate, sec)
	out := make([]float64, 0, len(samples)+len(gap))
	out = append(out, samples[:cut]...)
	out = append(out, gap...)
	out = append(out, samples[cut:]...)
	return out
}

// Negative control: a long mid-chart fade (>= 5 s without keying) with
// keying resuming afterwards is NOT an end of transmission. The crop
// must not fire at the fade, and the content after the fade must
// decode. The guard is structural: the crop point is the end of the
// LAST keyed window, so a mid-chart fade can never become the crop
// point while later keying exists. Teeth check: a first-unkeyed-run
// detector variant fails this test (crops at the fade, loses the
// post-fade content).
func TestWEFAXMidChartFadeNoTruncation(t *testing.T) {
	const rate = 8000
	full := synthWEFAXExactLines(rate, 60, 80, 3)      // 140 lines
	samples := insertGap(full, rate, 0.5, 3, 60+40, 8) // fade after 40 image lines
	pngData, rep, err := WEFAXImageDetailed(samples, rate, 120, 576)
	if err != nil {
		t.Fatalf("decode failed: %v (report %+v)", err, rep)
	}
	if rep.KeyingEndSec != 0 {
		t.Errorf("KeyingEndSec = %.1f: cropped at the mid-chart fade (keying resumed afterwards)", rep.KeyingEndSec)
	}
	img, err := png.Decode(bytes.NewReader(pngData))
	if err != nil {
		t.Fatal(err)
	}
	// 140 content lines + ~16 fade lines; the post-fade image lines
	// (40 of them) must all be present.
	if h := img.Bounds().Dy(); h < 140+14 {
		t.Errorf("height %d: content after the mid-chart fade lost (want ~156)", h)
	}
}

// Negative control: with a mid-chart fade AND a dead tail, the crop
// lands at the true end of keying, not at the fade, and the content
// between fade and tail survives.
func TestWEFAXMidChartFadeTailCropsAtTrueEnd(t *testing.T) {
	const rate = 8000
	full := synthWEFAXExactLines(rate, 60, 80, 3)
	gapped := insertGap(full, rate, 0.5, 3, 60+40, 8)
	txSec := 3.0 + float64(60+80)*0.5 + 8
	samples := appendHeldTone(gapped, rate, 30)
	pngData, rep, err := WEFAXImageDetailed(samples, rate, 120, 576)
	if err != nil {
		t.Fatalf("decode failed: %v (report %+v)", err, rep)
	}
	if rep.KeyingEndSec <= 0 || math.Abs(rep.KeyingEndSec-txSec) > 3 {
		t.Errorf("KeyingEndSec = %.1f s, want within 3 s of the true keying end %.0f s (crop must skip the mid-chart fade)", rep.KeyingEndSec, txSec)
	}
	img, err := png.Decode(bytes.NewReader(pngData))
	if err != nil {
		t.Fatal(err)
	}
	// The fade sits after line 100; cropping there would give ~100
	// rows. The true end gives ~140 content + ~16 fade rows.
	if h := img.Bounds().Dy(); h < 140+14 || h > 140+16+6 {
		t.Errorf("height %d, want ~156 (post-fade content present, held-tone tail cropped)", h)
	}
}
