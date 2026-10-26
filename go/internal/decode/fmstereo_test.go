package decode

import (
	"math"
	"testing"
)

// Synthetic MPX built here: L=1 kHz tone, R=2.5 kHz tone, pilot 10%.
func synthMPX(rate, n int, pilot bool, phase float64) (mpx []float64) {
	mpx = make([]float64, n)
	for i := range mpx {
		t := float64(i) / float64(rate)
		l := 0.8 * math.Sin(2*math.Pi*1000*t)
		r := 0.8 * math.Sin(2*math.Pi*2500*t)
		v := 0.45*(l+r) + 0.45*(l-r)*math.Sin(2*math.Pi*38000*t+2*phase)
		if pilot {
			v += 0.1 * math.Sin(2*math.Pi*19000*t+phase)
		}
		mpx[i] = v
	}
	return
}

func tonePow(x []float64, rate int, f float64) float64 {
	return goertzelPower(x[len(x)/4:], rate, f)
}

func TestFMStereoSeparation(t *testing.T) {
	rate := 192000
	for _, ph := range []float64{0, 0.9, -2.0} {
		res := DecodeFMStereo(synthMPX(rate, rate, true, ph), rate)
		if !res.PilotLocked {
			t.Fatalf("phase %.1f: pilot not locked (snr %.1f)", ph, res.PilotSNRdB)
		}
		// L must carry 1 kHz not 2.5 kHz and vice versa: >20 dB separation.
		lw, lx := tonePow(res.L, rate, 1000), tonePow(res.L, rate, 2500)
		rw, rx := tonePow(res.R, rate, 2500), tonePow(res.R, rate, 1000)
		if 10*math.Log10(lw/lx) < 20 || 10*math.Log10(rw/rx) < 20 {
			t.Fatalf("phase %.1f: separation L %.1f dB R %.1f dB", ph,
				10*math.Log10(lw/lx), 10*math.Log10(rw/rx))
		}
	}
}

func TestFMStereoNoPilotIsMono(t *testing.T) {
	rate := 192000
	res := DecodeFMStereo(synthMPX(rate, rate, false, 0), rate)
	if res.PilotLocked {
		t.Fatal("locked without pilot")
	}
	for i := range res.L {
		if res.L[i] != res.R[i] {
			t.Fatal("mono fallback differs")
		}
	}
}
