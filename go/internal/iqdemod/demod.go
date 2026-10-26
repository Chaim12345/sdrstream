// Package iqdemod turns a remote IQ stream into the demodulated-audio PCM the
// sdrstream pipeline consumes ([]float64 at the decode rate). Chain: complex
// rotator tuner -> decimating low-pass FIR -> mode demodulator. Pure Go, no
// cgo - Android-safe.
package iqdemod

import "math"

type Mode int

const (
	AM Mode = iota
	NBFM
	USB
	LSB
)

// Demodulator is a streaming tuner+decimator+demod for one channel.
type Demodulator struct {
	mode   Mode
	inRate float64
	dec    int

	// rotator: exp(j*phase), advancing by step per input sample
	phase float64
	step  float64

	fir   []float64
	state []complex64 // last len(fir)-1 input samples, oldest first

	// demod state
	prevIQ complex64 // FM discriminator
	dcR    float64   // AM DC-block high-pass state
	dcX    float64

	// SSB post-shift (filter method): after sideband selection, translate the
	// surviving sideband to 0..+bw (USB) or -bw..0 (LSB) before real().
	postPhase float64
	postStep  float64
}

// New builds a channel: inRate Hz IQ, a signal offsetHz above the IQ center is
// moved to baseband, low-passed and decimated to outRate, then demodulated.
// For SSB the filter method is used: the rotator places the carrier at the
// passband EDGE (bw = 0.45*outRate), so the wanted sideband straddles DC, the
// FIR rejects the carrier and opposite sideband, and real() recovers audio.
func New(inRate, outRate int, offsetHz float64, mode Mode) *Demodulator {
	dec := inRate / outRate
	if dec < 1 {
		dec = 1
	}
	d := &Demodulator{mode: mode, inRate: float64(inRate), dec: dec}
	eff := offsetHz
	bw := 0.45 * float64(outRate)
	taps := 8*dec + 1
	switch mode {
	case USB:
		eff = offsetHz + bw // carrier lands at -bw (edge), USB at -bw..0
		d.postStep = 2 * math.Pi * bw / float64(outRate) // then 0..+bw
		taps = 96*dec + 1 // sideband edge rejection needs a long filter
	case LSB:
		eff = offsetHz - bw // carrier at +bw, LSB at 0..+bw
		d.postStep = -2 * math.Pi * bw / float64(outRate)
		taps = 96*dec + 1
	}
	// rotator: multiply by exp(-j*2*pi*eff/inRate) per sample
	d.step = -2 * math.Pi * eff / float64(inRate)
	cut := bw / float64(inRate) // cycles/sample; carrier sits at the edge for SSB
	if taps%2 == 0 {
		taps++
	}
	d.fir = lowpass(taps, cut)
	d.state = make([]complex64, 0, len(d.fir)-1)
	return d
}

// lowpass designs a Hamming-windowed-sinc FIR, unity DC gain.
func lowpass(taps int, cut float64) []float64 {
	h := make([]float64, taps)
	m := float64(taps-1) / 2
	sum := 0.0
	for k := 0; k < taps; k++ {
		x := float64(k) - m
		var s float64
		if x == 0 {
			s = 2 * cut
		} else {
			s = math.Sin(2*math.Pi*cut*x) / (math.Pi * x)
		}
		w := 0.54 - 0.46*math.Cos(2*math.Pi*float64(k)/float64(taps-1))
		h[k] = s * w
		sum += h[k]
	}
	for k := range h {
		h[k] /= sum
	}
	return h
}

// Process consumes iq and appends demodulated audio to out, returning the
// extended slice. Streaming: rotator phase, FIR state and demod state carry
// across calls.
func (d *Demodulator) Process(iq []complex64, out []float64) []float64 {
	// 1. rotate (tune) into a temp stream joined with FIR state
	n := len(d.state) + len(iq)
	x := make([]complex64, n)
	copy(x, d.state)
	for i, v := range iq {
		re, im := float64(real(v)), float64(imag(v))
		c, s := math.Cos(d.phase), math.Sin(d.phase)
		x[len(d.state)+i] = complex64(complex(re*c-im*s, re*s+im*c))
		d.phase += d.step
		if d.phase > math.Pi {
			d.phase -= 2 * math.Pi
		} else if d.phase < -math.Pi {
			d.phase += 2 * math.Pi
		}
	}
	// 2. FIR + decimate. Output index k consumes input centered at k*dec + m.
	avail := n - (len(d.fir) - 1) // number of full filter positions
	if avail <= 0 {
		d.state = append(d.state[:0], x...)
		return out
	}
	pos := 0
	for ; pos < avail; pos += d.dec {
		var acc complex128
		for j := 0; j < len(d.fir); j++ {
			v := x[pos+j]
			w := d.fir[j]
			acc += complex(float64(real(v))*w, float64(imag(v))*w)
		}
		out = append(out, d.demod(complex64(acc)))
		_ = pos
	}
	// 3. keep FIR tail for next call
	tail := n - pos
	if tail > len(d.fir)-1 {
		tail = len(d.fir) - 1
	}
	d.state = append(d.state[:0], x[n-tail:]...)
	return out
}

func (d *Demodulator) demod(z complex64) float64 {
	switch d.mode {
	case NBFM:
		// quadrature discriminator: arg(z * conj(prev))
		p := z * cmplxConj(d.prevIQ)
		d.prevIQ = z
		return math.Atan2(float64(imag(p)), float64(real(p))) / math.Pi
	case USB, LSB:
		c, sn := math.Cos(d.postPhase), math.Sin(d.postPhase)
		re, im := float64(real(z)), float64(imag(z))
		z2 := complex64(complex(re*c-im*sn, re*sn+im*c))
		d.postPhase += d.postStep
		if d.postPhase > math.Pi {
			d.postPhase -= 2 * math.Pi
		} else if d.postPhase < -math.Pi {
			d.postPhase += 2 * math.Pi
		}
		return float64(real(z2))
	default: // AM
		env := math.Sqrt(float64(real(z)*real(z) + imag(z)*imag(z)))
		// DC-block high-pass: y[n] = x[n] - x[n-1] + 0.995*y[n-1]
		y := env - d.dcX + 0.995*d.dcR
		d.dcX, d.dcR = env, y
		return y
	}
}

func cmplxConj(z complex64) complex64 { return complex(real(z), -imag(z)) }
