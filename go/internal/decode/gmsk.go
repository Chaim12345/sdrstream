package decode

import (
	"math"
)

// gmsk.go: baseband data slicers for the FM-data modulations used by
// amateur satellites, in the spirit of the gr-satellites demodulators but
// written for the post-discriminator audio SatNOGS stations publish.
//
// GMSK (and plain FSK carrying NRZI HDLC) reaches a receiver's audio
// output as a low-passed NRZ waveform: the FM discriminator has already
// turned frequency shifts back into level shifts. Recovering the bits is
// therefore a clock-recovery problem, not a carrier problem: find the bit
// centers, slice the waveform, NRZI-decode and look for HDLC frames,
// exactly the structure of the Bell 202 AFSK demodulator in afsk.go but
// with a one-tap "correlator" (the waveform itself) instead of the two
// tone correlators.

// basebandCondition removes DC, normalises amplitude and low-passes the
// waveform so the slicer sees clean levels. The one-pole sections are
// enough: the HDLC CRC is the real arbiter of a good decode.
func basebandCondition(x []float64, rate, baud int) []float64 {
	if len(x) == 0 {
		return x
	}
	out := make([]float64, len(x))
	// DC blocker: y = x - xPrev + r*yPrev, r tuned to a few Hz.
	r := math.Exp(-2 * math.Pi * 20 / float64(rate))
	var xPrev, yPrev float64
	for i, v := range x {
		y := v - xPrev + r*yPrev
		xPrev, yPrev = v, y
		out[i] = y
	}
	// Low-pass at ~0.75*baud with a biquad Butterworth. Skip the filter
	// when the baud rate is too close to Nyquist for it to be meaningful.
	fc := 0.75 * float64(baud)
	if fc < float64(rate)*0.45 {
		k := math.Tan(math.Pi * fc / float64(rate))
		norm := 1 / (1 + math.Sqrt2*k + k*k)
		b0, b1, b2 := k*k*norm, 2*k*k*norm, k*k*norm
		a1 := 2 * (k*k - 1) * norm
		a2 := (1 - math.Sqrt2*k + k*k) * norm
		var x1, x2, y1, y2 float64
		for i, v := range out {
			y := b0*v + b1*x1 + b2*x2 - a1*y1 - a2*y2
			x2, x1 = x1, v
			y2, y1 = y1, y
			out[i] = y
		}
	}
	// Normalise to unit RMS so the slicer's thresholds are absolute.
	var power float64
	for _, v := range out {
		power += v * v
	}
	if power > 0 {
		gain := 1 / math.Sqrt(power/float64(len(out)))
		for i := range out {
			out[i] *= gain
		}
	}
	return out
}

// sliceBits recovers NRZI-decoded bits from a baseband waveform at the
// given baud rate, reusing the proportional-PLL clock recovery of
// fskDemodOpt: the phase accumulator wraps once per bit and nudges itself
// on level transitions.
func sliceBits(x []float64, rate, baud int, pllDiv uint32, invert bool) []bool {
	if baud <= 0 || rate < 2*baud || len(x) < rate/baud*8 {
		return nil
	}
	// subSamp=1: one waveform sample per step. sphaseInc wraps once per bit.
	const subSamp = 1
	sphaseInc := uint32(uint64(0x10000) * uint64(baud) * subSamp / uint64(rate))
	if sphaseInc == 0 {
		return nil
	}
	var bits []bool
	var shreg uint32
	var sphase uint32
	var lasts uint8
	for _, s := range x {
		v := s
		if invert {
			v = -v
		}
		shreg = (shreg << 1) | uint32(boolToInt(v > 0))
		if (shreg^(shreg>>1))&1 == 1 {
			if sphase < 0x8000-sphaseInc/2 {
				sphase += sphaseInc / pllDiv
			} else {
				sphase -= sphaseInc / pllDiv
			}
		}
		sphase += sphaseInc
		if sphase >= 0x10000 {
			sphase &= 0xFFFF
			lasts = (lasts << 1) | uint8(shreg&1)
			// NRZI decode: transition = 0 bit, held level = 1.
			curbit := (lasts ^ (lasts >> 1) ^ 1) & 1
			bits = append(bits, curbit == 1)
		}
	}
	return bits
}

// gmskVariants returns the slicer settings tried for one waveform. Level
// inversion is included because a receiver's discriminator polarity is
// unknown; the CRC decides which orientation was right.
func gmskVariants() []struct {
	pllDiv uint32
	invert bool
} {
	var v []struct {
		pllDiv uint32
		invert bool
	}
	for _, p := range []uint32{8, 4, 16} {
		v = append(v, struct {
			pllDiv uint32
			invert bool
		}{p, false}, struct {
			pllDiv uint32
			invert bool
		}{p, true})
	}
	return v
}

// GMSKBauds is the default search list: the rates amateur satellites
// actually use for GMSK/FM-data AX.25 (9600 most cubesats, 4800 e.g.
// CAS-4, 1200 some older birds).
var GMSKBauds = []int{9600, 4800, 2400, 1200}

// GMSKFrames demodulates GMSK/FM-data AX.25 from baseband audio: the
// audio a SatNOGS station publishes for an FM-mode observation, or the
// discriminator output of any FM receiver. Every baud rate and slicer
// variant runs; CRC-valid frames are de-duplicated the way
// ensembleFrames does, so repeated transmissions stay repeated.
func GMSKFrames(samples []float64, rate int, bauds []int) []AX25 {
	if len(bauds) == 0 {
		bauds = GMSKBauds
	}
	best := map[string]int{}
	first := map[string]AX25{}
	var order []string
	for _, baud := range bauds {
		if rate < 2*baud {
			continue
		}
		wave := basebandCondition(samples, rate, baud)
		for _, variant := range gmskVariants() {
			seen := map[string]int{}
			for _, f := range hdlcFrames(sliceBits(wave, rate, baud, variant.pllDiv, variant.invert)) {
				ax, ok := parseAX25(f)
				if !ok {
					continue
				}
				k := string(f)
				seen[k]++
				if _, ok := first[k]; !ok {
					first[k] = ax
					order = append(order, k)
				}
			}
			for k, n := range seen {
				if n > best[k] {
					best[k] = n
				}
			}
		}
	}
	var out []AX25
	for _, k := range order {
		for i := 0; i < best[k]; i++ {
			out = append(out, first[k])
		}
	}
	return out
}
