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
	// Per-bin expectation: the tone bin holds the signal power A^2/4 (plus
	// a negligible noise share), each floor bin holds sigma^2/n, so the
	// excess-over-floor ratio is n*A^2/(4*sigma^2). No bandwidth
	// correction applies: tone and floor bins share the analysis
	// bandwidth.
	toneAmp, noiseAmp := 8000.0, 800.0
	n := 4 * rate
	want := 10 * math.Log10(float64(n)*toneAmp*toneAmp/(4*noiseAmp*noiseAmp))
	samples := synthTone(rate, 4, 700, toneAmp, noiseAmp, 1)
	got, ok := EstimateToneSNR(samples, rate, 700)
	if !ok {
		t.Fatal("not ok")
	}
	if math.Abs(got-want) > 3 {
		t.Fatalf("SNR estimate %.1f dB, want ~%.1f dB (per-bin tone vs floor)", got, want)
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

// TestEstimateToneSNRNoBandwidthPenalty is the regression test for the QA
// finding on PR #7: on a live receiver the 10 MHz time-signal carrier stood
// +17.3 dB over the neighbouring floor in an FFT of the capture, but the
// estimator's 10*log10(2*rate/(n*500)) "500 Hz normalisation" (-31.8 dB at
// n=72000) reported -23.2 dB. A tone +17 dB over its floor must read about
// +17 dB, never negative.
func TestEstimateToneSNRNoBandwidthPenalty(t *testing.T) {
	const rate = 12000
	// n*A^2/(4*sigma^2) = 50 puts the per-bin tone/floor ratio at ~17 dB on
	// a 5 s, 12 kHz buffer (the beacon listen shape from the QA run).
	noiseAmp := 2000.0
	toneAmp := math.Sqrt(50 * 4 * noiseAmp * noiseAmp / (5 * rate))
	samples := synthTone(rate, 5, 700, toneAmp, noiseAmp, 7)
	got, ok := EstimateToneSNR(samples, rate, 700)
	if !ok {
		t.Fatal("not ok")
	}
	if got < 14 || got > 20 {
		t.Fatalf("a per-bin +17 dB tone read %.1f dB; the estimate must stay near +17, not be pulled negative", got)
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
