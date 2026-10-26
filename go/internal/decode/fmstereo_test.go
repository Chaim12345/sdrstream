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

// Full-band check: tones at 14 kHz (near the 15 kHz audio edge, so the
// L-R sideband reaches 52 kHz) must still separate, and a rate below the
// minimum must not pretend to decode.
func TestFMStereoFullBand(t *testing.T) {
	rate := 192000
	n := rate
	mpx := make([]float64, n)
	for i := range mpx {
		tt := float64(i) / float64(rate)
		l := 0.8 * math.Sin(2*math.Pi*14000*tt)
		r := 0.8 * math.Sin(2*math.Pi*9000*tt)
		mpx[i] = 0.45*(l+r) + 0.45*(l-r)*math.Sin(2*math.Pi*38000*tt+0.6) + 0.1*math.Sin(2*math.Pi*19000*tt+0.3)
	}
	res := DecodeFMStereo(mpx, rate)
	if !res.PilotLocked {
		t.Fatal("no lock")
	}
	lw, lx := tonePow(res.L, rate, 14000), tonePow(res.L, rate, 9000)
	rw, rx := tonePow(res.R, rate, 9000), tonePow(res.R, rate, 14000)
	t.Logf("L sep %.1f dB, R sep %.1f dB; L14k %.2e R9k %.2e", 10*math.Log10(lw/lx), 10*math.Log10(rw/rx), lw, rw)
	if 10*math.Log10(lw/lx) < 15 || 10*math.Log10(rw/rx) < 15 {
		t.Fatal("full-band separation too low")
	}
}

func TestFMStereoRejectsLowRate(t *testing.T) {
	res := DecodeFMStereo(synthMPX(76000, 76000, true, 0), 76000)
	if res.PilotLocked || res.L != nil {
		t.Fatal("decoded below minimum rate")
	}
}
