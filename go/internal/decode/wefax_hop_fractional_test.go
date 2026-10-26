package decode

import (
	"bytes"
	"image/png"
	"math"
	"math/rand"
	"testing"

	"github.com/chaim12345/sdrstream/go/internal/testutil/wefaxfixture"
)

// synthWEFAXExactLines synthesizes F1C audio whose line boundaries
// land at exact fractional sample positions (rounded per line), so
// non-integer rates like 7119 Hz carry true 0.5 s lines. The shared
// wefaxfixture truncates line length to whole samples, which at
// 7119 Hz would fake a 140 ppm transmitter drift; this local helper
// exists so that tuned gate fixtures keep their exact legacy meaning.
func synthWEFAXExactLines(rate float64, phasingLines, imageLines int, startToneSec float64) []float64 {
	const lineSec = 0.5
	lineSamplesF := lineSec * rate
	totalLines := phasingLines + imageLines
	startToneN := int(math.Round(startToneSec * rate))
	n := startToneN + int(math.Round(float64(totalLines)*lineSamplesF))
	out := make([]float64, n)
	rng := rand.New(rand.NewSource(42))
	phase := 0.0
	line := 0
	for i := 0; i < n; i++ {
		freq := 300.0
		if i >= startToneN {
			k := i - startToneN
			for line+1 < totalLines && float64(k) >= math.Round(float64(line+1)*lineSamplesF) {
				line++
			}
			lineStart := math.Round(float64(line) * lineSamplesF)
			lineEnd := math.Round(float64(line+1) * lineSamplesF)
			frac := (float64(k) - lineStart) / (lineEnd - lineStart)
			grey := 0.0
			switch {
			case line < phasingLines:
				if frac < 0.05 {
					grey = 1.0 // phasing wedge
				}
			default:
				il := line - phasingLines
				if il < imageLines/2 {
					if math.Abs(frac-float64(il)/float64(imageLines/2)) < 0.08 {
						grey = 1.0
					}
				} else if (int(frac*20)+il)%2 == 0 {
					grey = 1.0
				}
			}
			freq = 1500.0 + grey*(2300.0-1500.0)
		}
		phase += 2 * math.Pi * freq / rate
		out[i] = 0.6*math.Sin(phase) + 0.05*(rng.Float64()*2-1)
	}
	return out
}

// Regression: at rates where 0.002*rate is not an integer (7119 Hz
// WebSDR), the demod hop truncated to 14 samples while time math
// assumed 2 ms, so every decoded line consumed 3500 of 3559.5 samples
// (-8.36 ms/line walk, ~-16.7k ppm fitted drift). With exact line
// boundaries a correct decoder reports ~0 drift at ANY rate; integer
// rates are the controls that passed before and must keep passing.
func TestWEFAXNonIntegerHop7119(t *testing.T) {
	samples := synthWEFAXExactLines(7119, 60, 80, 3)
	pngData, rep, err := WEFAXImageDetailed(samples, 7119, 120, 576)
	if err != nil {
		t.Fatalf("decode failed: %v (report %+v)", err, rep)
	}
	img, err := png.Decode(bytes.NewReader(pngData))
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dy() < 80+60/2 {
		t.Fatalf("height %d, want >= %d", img.Bounds().Dy(), 80+60/2)
	}
	if w, _, _, _ := img.At(20, 10).RGBA(); w>>8 < 200 {
		t.Errorf("phasing wedge grey %d, want white", w>>8)
	}
	if math.Abs(rep.DriftPPM) > 500 {
		t.Errorf("drift %.0f ppm on clean 7119 Hz signal, want ~0 (hop truncation regression?)", rep.DriftPPM)
	}
	if math.Abs(rep.BlackHz-1500) > 15 || math.Abs(rep.WhiteHz-2300) > 15 {
		t.Errorf("tone lock %.0f/%.0f Hz, want 1500/2300", rep.BlackHz, rep.WhiteHz)
	}
}

func TestWEFAXIntegerHopControls(t *testing.T) {
	for _, rate := range []float64{7000, 8000, 12000} {
		samples := synthWEFAXExactLines(rate, 60, 60, 3)
		_, rep, err := WEFAXImageDetailed(samples, int(rate), 120, 576)
		if err != nil {
			t.Fatalf("rate %.0f: decode failed: %v", rate, err)
		}
		if math.Abs(rep.DriftPPM) > 500 {
			t.Errorf("rate %.0f: drift %.0f ppm on clean signal, want ~0", rate, rep.DriftPPM)
		}
	}
}

// The shared fixture keeps passing at its documented rates unchanged.
func TestWEFAXSharedFixtureUnaffected(t *testing.T) {
	f := wefaxfixture.Fixture{Rate: 12000, PhasingLines: 40, ImageLines: 40, StartToneSec: 2}
	if _, _, err := WEFAXImageDetailed(wefaxfixture.Encode(f), int(f.Rate), 120, 576); err != nil {
		t.Fatalf("shared fixture decode failed: %v", err)
	}
}
