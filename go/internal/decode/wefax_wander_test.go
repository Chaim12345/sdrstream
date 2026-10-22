package decode

import (
	"bytes"
	"image/png"
	"math"
	"math/rand"
	"testing"
)

// encodeWEFAXWandering synthesizes F1C audio whose "recorder clock"
// wanders sinusoidally (+-wanderPPM around a fixed offset), the failure
// mode of real airchecks that a single fitted line period cannot
// deskew. Returns the audio and the true line-start curve in samples.
func encodeWEFAXWandering(rate float64, lines int, offsetPPM, wanderPPM, wanderPeriodLines float64) ([]float64, []float64) {
	if rate == 0 {
		rate = 12000
	}
	lineSamples := rate * 0.5
	// True per-line period wanders.
	per := make([]float64, lines)
	start := make([]float64, lines+1)
	for l := 0; l < lines; l++ {
		ppm := offsetPPM + wanderPPM*math.Sin(2*math.Pi*float64(l)/wanderPeriodLines)
		per[l] = lineSamples * (1 + ppm/1e6)
		start[l+1] = start[l] + per[l]
	}
	n := int(start[lines]) + int(rate)/10
	out := make([]float64, n)
	rng := rand.New(rand.NewSource(11))
	// Image content: vertical bars of varying width (slant-visible).
	barAt := func(frac float64) float64 {
		v := 0.0
		for k := 1; k <= 5; k++ {
			c := float64(k) / 6.0
			w := 0.015 + 0.02*float64(k)
			if math.Abs(frac-c) < w {
				v = 1
			}
		}
		// a diagonal band for texture
		return v
	}
	phase := 0.0
	for i := 0; i < n; i++ {
		t := float64(i)
		// find current line
		l := sort_SearchF(start, t)
		if l >= lines {
			l = lines - 1
		}
		frac := (t - start[l]) / per[l]
		grey := 0.0
		if l < 30 {
			if frac < 0.05 {
				grey = 1 // phasing wedge
			}
		} else {
			grey = barAt(frac)
			if math.Abs(frac-math.Mod(float64(l), 97)/97.0) < 0.03 {
				grey = 1 - grey // diagonal texture both polarities
			}
		}
		freq := 1500 + grey*(2300-1500)
		phase += 2 * math.Pi * freq / rate
		out[i] = 0.6*math.Sin(phase) + 0.15*(rng.Float64()*2-1)
	}
	return out, start
}

func sort_SearchF(v []float64, x float64) int {
	lo, hi := 0, len(v)-1
	for lo < hi {
		mid := (lo + hi) / 2
		if v[mid+1] <= x {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}

// TestWEFAXWanderingClockDeskew drives the decoder with a capture whose
// clock wanders +-2500 ppm with a 90-line period (the archive.org
// IOC576 demo swings even wider). A constant-period fit leaves the
// chart slanted; the tracked clock must render rows that correlate.
func TestWEFAXWanderingClockDeskew(t *testing.T) {
	rate := 12000.0
	samples, _ := encodeWEFAXWandering(rate, 200, 300, 2500, 90)
	pngData, rep, err := WEFAXImageDetailed(samples, int(rate), 120, 576)
	if err != nil {
		t.Fatalf("decode failed: %v (report %+v)", err, rep)
	}
	if rep.SyncSource != "phasing-wedge" {
		t.Errorf("sync %q, want phasing-wedge", rep.SyncSource)
	}
	if rep.DeskewCorr < 0.55 {
		t.Errorf("deskew row correlation %.2f, want >= 0.55 (slant left)", rep.DeskewCorr)
	}
	if rep.DriftMaxPPM < 1500 {
		t.Errorf("wander max %+.0f ppm, want the +-2500 ppm swing tracked", rep.DriftMaxPPM)
	}
	img, err := png.Decode(bytes.NewReader(pngData))
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dy() < 150 {
		t.Errorf("height %d, want most of 200 lines", img.Bounds().Dy())
	}
}

// TestWEFAXStartToneIOC576 checks the start alternation is found and
// selects IOC 576: 6 s of 300 Hz black/white keying before phasing.
func TestWEFAXStartToneIOC576(t *testing.T) {
	rate := 12000.0
	// Build: 4 s of 300 Hz alternation + 40 phasing + 240 chart lines
	// (real proportions: the start tone is seconds of a minutes-long
	// transmission, so the tone lock is not pulled onto its sidebands).
	alt := int(4 * rate)
	lineSamples := int(rate * 0.5)
	lines := 280
	n := alt + lines*lineSamples
	out := make([]float64, n)
	rng := rand.New(rand.NewSource(3))
	phase := 0.0
	for i := 0; i < n; i++ {
		freq := 1500.0
		if i < alt {
			if math.Sin(2*math.Pi*300*float64(i)/rate) > 0 {
				freq = 2300
			}
		} else {
			k := i - alt
			l := k / lineSamples
			frac := float64(k%lineSamples) / float64(lineSamples)
			if l < 40 {
				if frac < 0.05 {
					freq = 2300
				}
			} else if int(frac*30)%3 == 0 {
				freq = 2300
			}
		}
		phase += 2 * math.Pi * freq / rate
		out[i] = 0.6*math.Sin(phase) + 0.1*(rng.Float64()*2-1)
	}
	_, rep, err := WEFAXImageDetailed(out, int(rate), 120, 576)
	if err != nil {
		t.Fatalf("decode failed: %v (report %+v)", err, rep)
	}
	if rep.StartToneIOC != 576 {
		t.Errorf("start tone IOC %d, want 576", rep.StartToneIOC)
	}
	if rep.StartToneSec > 1.0 {
		t.Errorf("start tone at %.1fs, want near 0", rep.StartToneSec)
	}
	if rep.PhasingStartSec < 3.5 || rep.PhasingStartSec > 6.0 {
		t.Errorf("phasing at %.1fs, want right after the 4 s start tone", rep.PhasingStartSec)
	}
}
