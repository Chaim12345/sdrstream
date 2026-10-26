package iqdemod

import (
	"math"
	"testing"
)

// goertzel returns the normalized power of freqHz in x (sample rate fs).
func goertzel(x []float64, fs, freq float64) float64 {
	n := float64(len(x))
	w := 2 * math.Pi * freq / fs
	var s float64
	for i, v := range x {
		s += v * math.Sin(w*float64(i))
	}
	var c float64
	for i, v := range x {
		c += v * math.Cos(w*float64(i))
	}
	return 2 * math.Sqrt(s*s+c*c) / n
}

// toneIQ synthesizes an IQ stream: a complex tone at (offset+modFreq) with
// the requested modulation applied to a carrier at offset.
func synthAM(inRate int, offset, modFreq, secs float64) []complex64 {
	n := int(float64(inRate) * secs)
	out := make([]complex64, n)
	for i := 0; i < n; i++ {
		t := float64(i) / float64(inRate)
		m := 1 + 0.8*math.Sin(2*math.Pi*modFreq*t)
		a := 2 * math.Pi * offset * t
		out[i] = complex(float32(m*math.Cos(a)), float32(m*math.Sin(a)))
	}
	return out
}

func synthFM(inRate int, offset, modFreq, dev, secs float64) []complex64 {
	n := int(float64(inRate) * secs)
	out := make([]complex64, n)
	phase := 0.0
	for i := 0; i < n; i++ {
		t := float64(i) / float64(inRate)
		inst := offset + dev*math.Sin(2*math.Pi*modFreq*t)
		phase += 2 * math.Pi * inst / float64(inRate)
		out[i] = complex(float32(math.Cos(phase)), float32(math.Sin(phase)))
	}
	return out
}

// synthSSB synthesizes a single-sideband tone at carrier offset + sideHz
// (upper sideband): an analytic signal with one spectral line.
func synthSSB(inRate int, offset, sideHz, secs float64) []complex64 {
	n := int(float64(inRate) * secs)
	out := make([]complex64, n)
	for i := 0; i < n; i++ {
		t := float64(i) / float64(inRate)
		a := 2 * math.Pi * (offset + sideHz) * t
		out[i] = complex(float32(math.Cos(a)), float32(math.Sin(a)))
	}
	return out
}

func process(d *Demodulator, iq []complex64) []float64 {
	out := make([]float64, 0, len(iq)/4)
	// feed in chunks to exercise streaming state
	for i := 0; i < len(iq); i += 4096 {
		j := i + 4096
		if j > len(iq) {
			j = len(iq)
		}
		out = d.Process(iq[i:j], out)
	}
	return out
}

func TestAMRecoversTone(t *testing.T) {
	in, out := 240000, 12000
	iq := synthAM(in, 60000, 1000, 0.5)
	d := New(in, out, 60000, AM)
	audio := process(d, iq)
	if got, want := len(audio), int(0.5*float64(out)); got < want-40 || got > want+5 {
		t.Fatalf("audio len %d want ~%d", got, want)
	}
	skip := audio[1200:] // skip settle
	p1k := goertzel(skip, float64(out), 1000)
	p15k := goertzel(skip, float64(out), 1500)
	if p1k < 10*p15k {
		t.Fatalf("AM: 1kHz power %.4f not dominant over 1.5kHz %.4f", p1k, p15k)
	}
}

func TestNBFMRecoversTone(t *testing.T) {
	in, out := 240000, 12000
	iq := synthFM(in, 50000, 1000, 3000, 0.5)
	d := New(in, out, 50000, NBFM)
	audio := process(d, iq)
	skip := audio[1200:]
	p1k := goertzel(skip, float64(out), 1000)
	p2k := goertzel(skip, float64(out), 2000)
	if p1k < 5*p2k {
		t.Fatalf("NBFM: 1kHz %.4f not dominant over 2kHz %.4f", p1k, p2k)
	}
}

func TestUSBRecoversSidebandAndRejectsImage(t *testing.T) {
	in, out := 240000, 12000
	// USB tone at carrier+1000 AND an opposite-sideband image at carrier-700.
	// A DSB (non-selective) demod would recover BOTH; filter-method USB must
	// keep 1 kHz and reject 700 Hz by >10x.
	n := in / 2
	iq := make([]complex64, n)
	usb := synthSSB(in, 70000, 1000, 0.5)
	lsb := synthSSB(in, 70000, -700, 0.5)
	for i := range iq {
		iq[i] = usb[i] + lsb[i]
	}
	d := New(in, out, 70000, USB)
	audio := process(d, iq)
	skip := audio[1200:]
	p1k := goertzel(skip, float64(out), 1000)
	p700 := goertzel(skip, float64(out), 700)
	if p1k < 10*p700 {
		t.Fatalf("USB: 1kHz %.4f vs image 700Hz %.4f - sideband not selected", p1k, p700)
	}
}

func TestLSBSelectsLowerSideband(t *testing.T) {
	in, out := 240000, 12000
	n := in / 2
	iq := make([]complex64, n)
	usb := synthSSB(in, 70000, 1000, 0.5)
	lsb := synthSSB(in, 70000, -700, 0.5)
	for i := range iq {
		iq[i] = usb[i] + lsb[i]
	}
	d := New(in, out, 70000, LSB)
	audio := process(d, iq)
	skip := audio[1200:]
	p1k := goertzel(skip, float64(out), 1000)
	p700 := goertzel(skip, float64(out), 700)
	if p700 < 10*p1k {
		t.Fatalf("LSB: 700Hz %.4f vs image 1kHz %.4f - sideband not selected", p700, p1k)
	}
}

func TestStreamingEqualsBlock(t *testing.T) {
	in, out := 240000, 12000
	iq := synthAM(in, 60000, 700, 0.2)
	a := New(in, out, 60000, AM)
	b := New(in, out, 60000, AM)
	block := a.Process(iq, nil)
	stream := process(b, iq)
	if len(block) != len(stream) {
		t.Fatalf("len %d vs %d", len(block), len(stream))
	}
	worst := 0.0
	for i := range block {
		d := math.Abs(block[i] - stream[i])
		if d > worst {
			worst = d
		}
	}
	if worst > 1e-6 {
		t.Fatalf("streaming diverges: %g", worst)
	}
}
