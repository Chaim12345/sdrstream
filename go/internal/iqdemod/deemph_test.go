package iqdemod

import (
	"math"
	"testing"
)

func sine(rate int, f float64, secs float64) []float64 {
	n := int(float64(rate) * secs)
	x := make([]float64, n)
	for i := range x {
		x[i] = math.Sin(2 * math.Pi * f * float64(i) / float64(rate))
	}
	return x
}

func ampAt(x []float64, rate int, f float64) float64 {
	w := 2 * math.Pi * f / float64(rate)
	var re, im float64
	for i, v := range x {
		re += v * math.Cos(w * float64(i))
		im += v * math.Sin(w * float64(i))
	}
	return 2 * math.Hypot(re, im) / float64(len(x))
}

// |H(f)| = 1/sqrt(1+(2*pi*f*tau)^2): -3 dB at 1/(2*pi*tau), and a computed
// attenuation at 10 kHz. The one-pole a=1-exp(-1/(rate*tau)) approximation
// sits slightly ABOVE the ideal RC curve near Nyquist (measured 0.326 vs
// ideal 0.303 at 10 kHz, 50us, 48 kHz) - inherent to this filter form, so
// the 10 kHz tolerance is 0.03 while the -3 dB point holds at 0.02.
func TestDeemphasisCurve(t *testing.T) {
	const rate = 48000
	for _, tau := range []float64{75e-6, 50e-6} {
		f3 := 1 / (2 * math.Pi * tau)
		got := ampAt(Deemphasis(sine(rate, f3, 0.5), rate, tau), rate, f3)
		if math.Abs(got-0.7071) > 0.02 {
			t.Fatalf("tau %v: at -3dB point %.0f Hz got amplitude %.3f, want ~0.707", tau, f3, got)
		}
		want10k := 1 / math.Sqrt(1+math.Pow(2*math.Pi*10000*tau, 2))
		got10k := ampAt(Deemphasis(sine(rate, 10000, 0.5), rate, tau), rate, 10000)
		if math.Abs(got10k-want10k) > 0.03 {
			t.Fatalf("tau %v: at 10 kHz got %.3f, want %.3f", tau, got10k, want10k)
		}
		// DC-ish content passes at unity.
		got100 := ampAt(Deemphasis(sine(rate, 100, 0.5), rate, tau), rate, 100)
		if math.Abs(got100-1) > 0.02 {
			t.Fatalf("tau %v: at 100 Hz got %.3f, want ~1.0", tau, got100)
		}
	}
}
