package kiwisdr

import (
	"math"
	"math/rand"
	"testing"
)

// synthTone builds rate-Hz audio with a tone of the given amplitude over
// Gaussian noise (all synthetic; no network, no recordings).
func synthTone(rate int, seconds float64, toneHz, toneAmp, noiseAmp float64, seed int64) []int16 {
	n := int(float64(rate) * seconds)
	out := make([]int16, n)
	rng := rand.New(rand.NewSource(seed))
	for i := range out {
		v := toneAmp*math.Sin(2*math.Pi*toneHz*float64(i)/float64(rate)) + noiseAmp*rng.NormFloat64()
		if v > 32767 {
			v = 32767
		}
		if v < -32768 {
			v = -32768
		}
		out[i] = int16(v)
	}
	return out
}

func TestEstimateToneSNR(t *testing.T) {
	const rate = 12000
	// True SNR in the estimator's 500 Hz reference bandwidth: tone power
	// A^2/2 against the share of the noise variance that falls in 500 Hz.
	toneAmp, noiseAmp := 8000.0, 800.0
	want := 10 * math.Log10(toneAmp*toneAmp/2/(noiseAmp*noiseAmp*beaconSNRBandwidthHz/rate))
	samples := synthTone(rate, 4, 700, toneAmp, noiseAmp, 1)
	got, ok := EstimateToneSNR(samples, rate, 700)
	if !ok {
		t.Fatal("not ok")
	}
	if math.Abs(got-want) > 3 {
		t.Fatalf("SNR estimate %.1f dB, want ~%.1f dB", got, want)
	}

	// A stronger tone must read higher, a missing tone much lower.
	strong, _ := EstimateToneSNR(synthTone(rate, 4, 700, toneAmp*4, noiseAmp, 2), rate, 700)
	if strong <= got {
		t.Fatalf("stronger tone scored lower: %.1f vs %.1f", strong, got)
	}
	quiet, _ := EstimateToneSNR(synthTone(rate, 4, 700, 0, noiseAmp, 3), rate, 700)
	if quiet > 3 {
		t.Fatalf("noise-only buffer should floor near/below 0 dB, got %.1f", quiet)
	}
}

func TestEstimateToneSNRRanksReceivers(t *testing.T) {
	const rate = 12000
	// The health table only needs a consistent ordering: the same synthetic
	// signal through a "good" and a "deaf" receiver must order correctly.
	good, _ := EstimateToneSNR(synthTone(rate, 4, 700, 9000, 700, 10), rate, 700)
	deaf, _ := EstimateToneSNR(synthTone(rate, 4, 700, 300, 2000, 11), rate, 700)
	if good <= deaf {
		t.Fatalf("good receiver (%.1f dB) must beat deaf one (%.1f dB)", good, deaf)
	}
}

func TestEstimateToneSNRShortBuffer(t *testing.T) {
	if _, ok := EstimateToneSNR(make([]int16, 100), 12000, 700); ok {
		t.Fatal("a 100-sample buffer must not produce an estimate")
	}
	if _, ok := EstimateToneSNR(nil, 12000, 700); ok {
		t.Fatal("nil buffer must not produce an estimate")
	}
}
