package decode

import (
	"math"
	"sort"
)

// bpsk.go: a BPSK demodulator for audio-IF recordings, built the way
// gr-satellites structures its BPSK chain (carrier recovery, symbol
// strobe, NRZI, HDLC) but with feedforward estimators instead of a
// feedback loop, because a squaring estimator cannot oscillate:
//
//  1. Carrier recovery: BPSK squared loses its modulation and leaves a
//     spectral line at twice the carrier, so a scan of x^2 finds the
//     exact carrier - including any tuning offset - without a search
//     grid at the symbol level.
//  2. Phase tracking: after mixing to baseband, the Viterbi & Viterbi
//     estimator for BPSK (half the argument of the running mean of z^2)
//     removes the residual rotation and the 180-degree ambiguity is
//     covered by trying both polarities; the HDLC CRC picks the winner.
//  3. Symbol timing: the sign of the derotated in-phase arm feeds the
//     same fixed-point bit PLL the AFSK and GMSK demodulators use.
//
// This applies when a station recorded a BPSK downlink as an SSB audio
// tone. For SatNOGS FM-mode audio of BPSK satellites the discriminator
// has destroyed the phase and this decoder does not apply.

// squaredProfile is the power spectrum of x^2 over [lo2, hi2] Hz: the
// raw material for carrier recovery. Squaring strips the BPSK modulation
// and leaves a line at twice the carrier - but also the symbol clock and
// its harmonics, because the filtered symbol amplitude is not constant
// through transitions. Callers must skip those known lines.
func squaredProfile(x []float64, rate int, lo2, hi2, step float64) (freqs, powers []float64) {
	n := len(x)
	if n > 5*rate {
		n = 5 * rate // the first seconds are enough for a stable estimate
	}
	if n < rate/20 {
		return nil, nil
	}
	for f2 := lo2; f2 <= hi2; f2 += step {
		w := 2 * math.Pi * f2 / float64(rate)
		cw := 2 * math.Cos(w)
		var s0, s1, s2 float64
		for i := 0; i < n; i++ {
			s0 = x[i]*x[i] + cw*s1 - s2
			s2, s1 = s1, s0
		}
		freqs = append(freqs, f2)
		powers = append(powers, s1*s1+s2*s2-cw*s1*s2)
	}
	return freqs, powers
}

// carrierCandidates returns the strongest peaks of the squared profile
// as carrier candidates, strongest first. The squared signal contains
// the doubled carrier but also the symbol clock and its harmonics
// (filtered symbols are not constant through transitions), and which of
// those dominates depends on the pulse shape - so no peak is excluded a
// priori: every prominent line is tried and the HDLC CRC decides which
// one was the carrier. prominence is the peak's power over the profile
// mean; below 4 there is no line worth trying.
func carrierCandidates(freqs, powers []float64, maxCandidates int) []float64 {
	if len(freqs) == 0 {
		return nil
	}
	var sum float64
	for _, p := range powers {
		sum += p
	}
	mean := sum / float64(len(powers))
	type peak struct {
		f2         float64
		prominence float64
	}
	var peaks []peak
	for i, p := range powers {
		if p < 4*mean {
			continue
		}
		// Local maxima only: a Goertzel bin next to a strong line is a
		// shoulder, not a candidate.
		if i > 0 && powers[i-1] > p {
			continue
		}
		if i+1 < len(powers) && powers[i+1] > p {
			continue
		}
		peaks = append(peaks, peak{freqs[i], p / mean})
	}
	sort.Slice(peaks, func(a, b int) bool { return peaks[a].prominence > peaks[b].prominence })
	var out []float64
	for _, pk := range peaks {
		if len(out) >= maxCandidates {
			break
		}
		// Skip a peak within 1% of an already chosen one.
		dup := false
		for _, c := range out {
			if math.Abs(pk.f2-2*c) < 0.01*pk.f2 {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, pk.f2/2)
		}
	}
	return out
}

// BPSKBauds is the default symbol-rate search list for amateur BPSK
// downlinks.
var BPSKBauds = []int{1200, 2400, 4800, 9600}

// bpskBits mixes to baseband at carrier, tracks the residual phase with
// the Viterbi & Viterbi estimator and slices at the baud rate. It
// returns NRZI-decoded bits.
func bpskBits(x []float64, rate int, carrier, baud float64, invert bool) []bool {
	if len(x) < rate/20 {
		return nil
	}
	sphaseInc := uint32(uint64(0x10000) * uint64(baud) / uint64(rate))
	if sphaseInc == 0 {
		return nil
	}
	// Arm low-pass at baud/2, followed by a boxcar one symbol long: the
	// one-pole alone leaves too much of the 2*fc mixing product (it sits
	// only ~2x above the cutoff), and the ripple flips decisions near
	// zero crossings.
	lpA := 1 - math.Exp(-2*math.Pi*(baud/2)/float64(rate))
	box := int(float64(rate) / baud)
	if box < 1 {
		box = 1
	}
	iBuf := make([]float64, box)
	qBuf := make([]float64, box)
	var iSum, qSum float64
	bufPos := 0
	var iFilt, qFilt float64
	var phase float64
	// Residual-phase tracker, decision-directed: rotate the constellation
	// by phi, and steer phi with the quadrature remainder weighted by the
	// decision. Unlike a Viterbi & Viterbi estimator this has no pi
	// flips mid-frame; either stable point (0 or pi) decodes identically
	// after NRZI.
	var phi float64
	const mu = 0.05
	var shreg uint32
	var sphase uint32
	var lasts uint8
	var bits []bool
	var energy, samples int
	for _, s := range x {
		phase += 2 * math.Pi * carrier / float64(rate)
		if phase > math.Pi {
			phase -= 2 * math.Pi
		}
		iFilt += lpA * (s*math.Cos(phase) - iFilt)
		qFilt += lpA * (-s*math.Sin(phase) - qFilt)
		iSum += iFilt - iBuf[bufPos]
		qSum += qFilt - qBuf[bufPos]
		iBuf[bufPos] = iFilt
		qBuf[bufPos] = qFilt
		bufPos = (bufPos + 1) % box
		iArm := iSum / float64(box)
		qArm := qSum / float64(box)
		v := iArm*math.Cos(phi) + qArm*math.Sin(phi)
		w := -iArm*math.Sin(phi) + qArm*math.Cos(phi)
		if v >= 0 {
			phi += mu * w
		} else {
			phi -= mu * w
		}
		if invert {
			v = -v
		}
		shreg = (shreg << 1) | uint32(boolToInt(v > 0))
		if (shreg^(shreg>>1))&1 == 1 {
			if sphase < 0x8000-sphaseInc/2 {
				sphase += sphaseInc / 8
			} else {
				sphase -= sphaseInc / 8
			}
		}
		sphase += sphaseInc
		if sphase >= 0x10000 {
			sphase &= 0xFFFF
			energy += int(math.Abs(v) * 1000)
			samples++
			lasts = (lasts << 1) | uint8(shreg&1)
			curbit := (lasts ^ (lasts >> 1) ^ 1) & 1
			bits = append(bits, curbit == 1)
		}
	}
	if samples == 0 || float64(energy)/float64(samples) < 5 {
		return nil
	}
	return bits
}

// BPSKFrames demodulates BPSK AX.25 from an audio-IF recording. The
// carrier is measured from the signal itself over lo..hi Hz, every baud
// rate and both constellation polarities are tried, and the results are
// de-duplicated like the other ensemble demodulators.
func BPSKFrames(samples []float64, rate int, bauds []int, loHi [2]float64) []AX25 {
	if len(bauds) == 0 {
		bauds = BPSKBauds
	}
	if loHi == [2]float64{} {
		loHi = [2]float64{300, 16000}
	}
	// Scan step: fine enough that a carrier line cannot fall between bins
	// (a line is ~1/T wide, T the analysis window), coarse enough to keep
	// the sweep fast on long files.
	step := (loHi[1] - loHi[0]) / 1500
	if step < 4 {
		step = 4
	}
	freqs, powers := squaredProfile(samples, rate, 2*loHi[0], 2*loHi[1], step)
	if len(freqs) == 0 {
		return nil
	}
	carriers := carrierCandidates(freqs, powers, 6)
	if len(carriers) == 0 {
		return nil
	}
	best := map[string]int{}
	first := map[string]AX25{}
	var order []string
	for _, baud := range bauds {
		for _, carrier := range carriers {
			if carrier+float64(baud)/2+100 >= float64(rate)/2 {
				continue
			}
			for _, invert := range []bool{false, true} {
				seen := map[string]int{}
				for _, f := range hdlcFrames(bpskBits(samples, rate, carrier, float64(baud), invert)) {
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
	}
	var out []AX25
	for _, k := range order {
		for i := 0; i < best[k]; i++ {
			out = append(out, first[k])
		}
	}
	return out
}
