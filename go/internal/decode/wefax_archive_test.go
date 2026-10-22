package decode

import (
	"math"
	"testing"
)

// synthWEFAXTones keys random FSK between black and white at 100 hops/s,
// the texture the tone-pair lock looks for.
func synthWEFAXTones(black, white float64, seconds int, rate int) []float64 {
	n := seconds * rate
	out := make([]float64, n)
	hop := rate / 100
	seed := uint32(7)
	for i := 0; i < n; i++ {
		if i%hop == 0 {
			seed = seed*1664525 + 1013904223
		}
		f := black
		if seed&(1<<31) != 0 {
			f = white
		}
		out[i] = 0.6 * math.Sin(2*math.Pi*f*float64(i)/float64(rate))
	}
	return out
}

// TestWEFAXLockStandardTones keeps the standard 1500/2300 Hz lock exact.
func TestWEFAXLockStandardTones(t *testing.T) {
	rate := 8000
	s := synthWEFAXTones(1500, 2300, 8, rate)
	black, white, share, prom := wefaxLockTonePair(s, rate)
	if black < 1480 || black > 1520 || white < 2280 || white > 2320 {
		t.Errorf("standard lock = %.0f/%.0f Hz, want 1500/2300", black, white)
	}
	if prom < 2.5 || share < 0.05 {
		t.Errorf("standard lock weak: prominence %.1f share %.2f", prom, share)
	}
}

// TestWEFAXLockShiftedTones covers amateur/demo captures like the
// Internet Archive IOC576 demo, whose pair sits at 1200/1800 Hz.
func TestWEFAXLockShiftedTones(t *testing.T) {
	rate := 8000
	s := synthWEFAXTones(1200, 1800, 8, rate)
	black, white, share, prom := wefaxLockTonePair(s, rate)
	if black < 1180 || black > 1220 || white < 1780 || white > 1820 {
		t.Errorf("shifted lock = %.0f/%.0f Hz, want 1200/1800", black, white)
	}
	if prom < 2.5 || share < 0.05 {
		t.Errorf("shifted lock weak: prominence %.1f share %.2f", prom, share)
	}
}
