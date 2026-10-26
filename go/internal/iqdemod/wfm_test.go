package iqdemod

import (
	"math"
	"testing"
)

// synthWFM returns inRate IQ of an FM carrier at baseband center, modulated
// by a tone at toneHz with the given peak deviation.
func synthWFM(inRate int, toneHz, devHz float64, secs float64) []complex64 {
	n := int(float64(inRate) * secs)
	iq := make([]complex64, n)
	var phase float64
	ko := 2 * math.Pi * devHz / float64(inRate)
	wm := 2 * math.Pi * toneHz / float64(inRate)
	for i := range iq {
		phase += ko * math.Sin(wm*float64(i))
		iq[i] = complex(float32(math.Cos(phase)), float32(math.Sin(phase)))
	}
	return iq
}

func tonePower(x []float64, rate int, f float64) float64 {
	w := 2 * math.Pi * f / float64(rate)
	var re, im float64
	for i, v := range x {
		re += v * math.Cos(w*float64(i))
		im += v * math.Sin(w*float64(i))
	}
	n := float64(len(x))
	return (re*re + im*im) / (n * n)
}

// A 1 kHz tone at +/-75 kHz deviation must survive the WFM chain and beat
// the 1.5/2.5 kHz neighbours by a wide margin; the MPX rate must be stereo-
// capable (>= 114 kHz).
func TestWFMRecoversTone(t *testing.T) {
	const inRate = 1500000
	d := NewWFM(inRate)
	if d == nil {
		t.Fatal("NewWFM nil at 1.5 Msps")
	}
	mpxRate := inRate / d.dec
	if mpxRate < 150000 || mpxRate > 240000 {
		t.Fatalf("MPX rate %d outside [150k,240k]", mpxRate)
	}
	iq := synthWFM(inRate, 1000, 75000, 0.5)
	var mpx []float64
	for off := 0; off < len(iq); off += 65536 {
		end := off + 65536
		if end > len(iq) {
			end = len(iq)
		}
		mpx = d.Process(iq[off:end], mpx)
	}
	// skip filter settle (first 5%)
	mpx = mpx[len(mpx)/20:]
	p1 := tonePower(mpx, mpxRate, 1000)
	p15 := tonePower(mpx, mpxRate, 1500)
	p25 := tonePower(mpx, mpxRate, 2500)
	if p15 <= 0 {
		p15 = 1e-20
	}
	if p25 <= 0 {
		p25 = 1e-20
	}
	snr := 10 * math.Log10(p1 / ((p15 + p25) / 2))
	t.Logf("tone power %.6f neighbours %.6f/%.6f, SNR %.1f dB, mpxRate %d", p1, p15, p25, snr, mpxRate)
	if snr < 20 {
		t.Fatalf("1 kHz tone not dominant: SNR %.1f dB", snr)
	}
}

// Same tone through the NBFM chain (4.8 kHz channel at 48 kHz out) must lose
// it - this is the mode-mismatch the live A/B passes had.
func TestWFMModeMismatchLosesTone(t *testing.T) {
	const inRate = 1500000
	d := New(inRate, 48000, 0, NBFM)
	iq := synthWFM(inRate, 1000, 75000, 0.5)
	var af []float64
	for off := 0; off < len(iq); off += 65536 {
		end := off + 65536
		if end > len(iq) {
			end = len(iq)
		}
		af = d.Process(iq[off:end], af)
	}
	af = af[len(af)/20:]
	p1 := tonePower(af, 48000, 1000)
	p15 := tonePower(af, 48000, 1500)
	p25 := tonePower(af, 48000, 2500)
	snr := 10 * math.Log10(p1 / ((p15 + p25) / 2 + 1e-20))
	t.Logf("NBFM-on-broadcast SNR %.1f dB", snr)
	// Honest claim shape: the mismatch DEGRADES, it does not destroy. The
	// matched WFM chain measured ~60 dB on the same signal; require the
	// narrow chain to land at least 15 dB below that. An NBFM pass producing
	// dominant-tone audio is NOT evidence of matched quality.
	const wfmReferenceSNR = 60.0 // measured by TestWFMRecoversTone on this synth
	if snr > wfmReferenceSNR-15 {
		t.Fatalf("NBFM chain too close to matched chain: %.1f dB vs %.1f dB", snr, wfmReferenceSNR)
	}
}

func TestWFMRejectsLowRate(t *testing.T) {
	if NewWFM(96000) != nil {
		t.Fatal("96 kHz IQ cannot carry a broadcast channel")
	}
}
