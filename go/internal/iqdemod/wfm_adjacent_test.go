package iqdemod

import (
	"math"
	"testing"
)

// mixCarriers sums two FM carriers at different offsets into one IQ stream.
func mixCarriers(inRate int, aOffset, aTone, aDev, bOffset, bTone, bDev float64, secs float64) []complex64 {
	n := int(float64(inRate) * secs)
	iq := make([]complex64, n)
	var phA, phB float64
	wA := 2 * math.Pi * aOffset / float64(inRate)
	wB := 2 * math.Pi * bOffset / float64(inRate)
	kA := 2 * math.Pi * aDev / float64(inRate)
	kB := 2 * math.Pi * bDev / float64(inRate)
	mA := 2 * math.Pi * aTone / float64(inRate)
	mB := 2 * math.Pi * bTone / float64(inRate)
	for i := range iq {
		phA += wA + kA*math.Sin(mA*float64(i))
		phB += wB + kB*math.Sin(mB*float64(i))
		iq[i] = 0.5*complex(float32(math.Cos(phA)), float32(math.Sin(phA))) +
			0.5*complex(float32(math.Cos(phB)), float32(math.Sin(phB)))
	}
	return iq
}

// A same-strength broadcast interferer 300 kHz above the wanted station must
// not leak its audio into the wanted MPX through the decimation filter
// (alias rejection), and 53 kHz MPX content (stereo L-R top edge) must pass.
func TestWFMAdjacentChannelRejection(t *testing.T) {
	const inRate = 1500000
	d := NewWFM(inRate)
	if d == nil {
		t.Fatal("nil at 1.5 Msps")
	}
	mpxRate := inRate / d.dec
	iq := mixCarriers(inRate, 0, 1000, 75000, 300000, 2200, 75000, 0.5)
	var mpx []float64
	for off := 0; off < len(iq); off += 65536 {
		end := off + 65536
		if end > len(iq) {
			end = len(iq)
		}
		mpx = d.Process(iq[off:end], mpx)
	}
	mpx = mpx[len(mpx)/20:]
	want := tonePower(mpx, mpxRate, 1000)
	leak := tonePower(mpx, mpxRate, 2200)
	rej := 10 * math.Log10(want / (leak + 1e-20))
	t.Logf("wanted 1kHz %.6f, leaked 2.2kHz %.6f, rejection %.1f dB (mpxRate %d)", want, leak, rej, mpxRate)
	if rej < 20 {
		t.Fatalf("adjacent-channel leak too strong: %.1f dB", rej)
	}
}

// MPX content at 53 kHz (top of the stereo difference band) must survive the
// chain: deviate the carrier with a 53 kHz tone and require recovery.
func TestWFMPasses53kMPXEdge(t *testing.T) {
	const inRate = 1500000
	d := NewWFM(inRate)
	if d == nil {
		t.Fatal("nil at 1.5 Msps")
	}
	mpxRate := inRate / d.dec
	iq := synthWFM(inRate, 53000, 40000, 0.3)
	var mpx []float64
	for off := 0; off < len(iq); off += 65536 {
		end := off + 65536
		if end > len(iq) {
			end = len(iq)
		}
		mpx = d.Process(iq[off:end], mpx)
	}
	mpx = mpx[len(mpx)/20:]
	p53 := tonePower(mpx, mpxRate, 53000)
	p47 := tonePower(mpx, mpxRate, 47000)
	p60 := tonePower(mpx, mpxRate, 60000)
	snr := 10 * math.Log10(p53 / ((p47 + p60) / 2 + 1e-20))
	t.Logf("53kHz MPX edge SNR %.1f dB", snr)
	if snr < 15 {
		t.Fatalf("53 kHz MPX content lost: %.1f dB", snr)
	}
}
