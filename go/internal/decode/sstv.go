package decode

import (
	"context"
	"fmt"
	"math"
)

// SSTV (slow-scan television) sends a picture as an audio tone whose
// frequency maps to brightness. Every transmission begins with a VIS
// (Vertical Interval Signalling) header that names the mode, so a decoder
// can tell a real image from noise before spending time on the picture.
//
// VIS format: a 1900 Hz leader (~300 ms), a 1200 Hz break (~10 ms), a
// second 1900 Hz leader (~300 ms), then ten 30 ms signalling tones: a
// start bit, seven data bits LSB first (1100 Hz = 1, 1300 Hz = 0), an
// even-parity bit over the data bits, and a 1200 Hz stop bit. The start
// bit is 1200 Hz in the original Robot/G3OQD documentation and in the
// pysstv encoder, while some encoders send 1100 Hz; both are accepted.
const (
	sstvLeader = 1900.0
	sstvBreak  = 1200.0
	sstvBit0   = 1300.0
	sstvBit1   = 1100.0
	sstvBitLen = 0.030 // seconds
)

// sstvModes maps a 7-bit VIS code to a human mode name. Cross-checked
// against the QSSTV mode table (src/sstv/sstvparam.cpp, which stores the
// parity bit in bit 7), pysstv's VIS_CODE constants, and the G3OQD /
// barberdsp mode descriptions.
var sstvModes = map[int]string{
	0x02: "Robot 8 BW", 0x06: "Robot 12 BW", 0x0A: "Robot 24 BW", 0x0E: "Robot 36 BW",
	0x04: "Robot 24", 0x08: "Robot 36", 0x0C: "Robot 72",
	0x20: "Martin M4", 0x24: "Martin M3", 0x28: "Martin M2", 0x2C: "Martin M1",
	0x38: "Scottie S2", 0x3C: "Scottie S1", 0x4C: "Scottie DX",
	0x37: "Wraase SC2-180", 0x3B: "Wraase SC2-60", 0x3F: "Wraase SC2-120",
	0x5D: "PD 50", 0x63: "PD 90", 0x5F: "PD 120", 0x62: "PD 160",
	0x60: "PD 180", 0x61: "PD 240", 0x5E: "PD 290",
	0x40: "AVT 24", 0x44: "AVT 90", 0x48: "AVT 94",
	0x71: "Pasokon P3", 0x72: "Pasokon P5", 0x73: "Pasokon P7",
}

// SSTVResult is a detected SSTV header.
type SSTVResult struct {
	Mode     string
	VIS      int
	StartSec float64
	SnrdB    float64
}

// tonePurity returns, per hop, the share of window energy near freq:
// 2*g^2/(N*power) is ~1 for a pure tone and falls sharply as energy
// spreads. This is robust where instantaneous-frequency demodulation
// wobbles, notably on 1200 Hz sync tones.
func tonePurity(samples []float64, rate int, freq, winSec, hopSec float64) []float64 {
	out, _ := tonePurityContext(context.Background(), samples, rate, freq, winSec, hopSec)
	return out
}

// tonePurityContext is tonePurity with cancellation checks between hops.
func tonePurityContext(ctx context.Context, samples []float64, rate int, freq, winSec, hopSec float64) ([]float64, error) {
	win := int(winSec * float64(rate))
	hop := int(hopSec * float64(rate))
	if win < 8 || hop < 1 {
		return nil, nil
	}
	n := (len(samples) - win) / hop
	if n <= 0 {
		return nil, nil
	}
	out := make([]float64, n)
	for h := 0; h < n; h++ {
		if h&255 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		seg := samples[h*hop : h*hop+win]
		var power float64
		for _, v := range seg {
			power += v * v
		}
		if power <= 1e-9*float64(win) {
			out[h] = 0
			continue
		}
		g := goertzel(seg, rate, freq)
		out[h] = 2 * g * g / (float64(win) * power)
	}
	return out, nil
}

// SSTVDetect scans audio for SSTV VIS headers and returns each one found
// with the mode it names.
//
// False-positive defences, in order: both leaders must hold a pure
// 1900 Hz tone for ~290 ms, checked per 2 ms hop (image data always
// carries a 1200 Hz line sync inside any 300 ms window, and swept video
// tones wander out of the band); the start and stop bits must be correct;
// the data bits must pass even parity; and when enough audio follows the
// header, the image must show a consistent line-sync cadence.
func SSTVDetect(samples []float64, rate int) []SSTVResult {
	out, _ := sstvDetectContext(context.Background(), samples, rate)
	return out
}

// sstvDetectContext is SSTVDetect with cancellation checks inside the
// purity scans and the header scan loop.
func sstvDetectContext(ctx context.Context, samples []float64, rate int) ([]SSTVResult, error) {
	if rate < 8000 || len(samples) < rate {
		return nil, nil
	}
	const hopSec, winSec = 0.002, 0.008
	p1200, err := tonePurityContext(ctx, samples, rate, sstvBreak, winSec, hopSec)
	if err != nil {
		return nil, err
	}
	p1900, err := tonePurityContext(ctx, samples, rate, sstvLeader, winSec, hopSec)
	if err != nil {
		return nil, err
	}
	if len(p1200) == 0 {
		return nil, nil
	}
	hopN := int(hopSec * float64(rate))
	bitN := int(sstvBitLen * float64(rate))
	leaderHops := int(0.290 / hopSec) // ~290 ms of leader each side
	var demod []float64               // computed lazily on the first header candidate
	var out []SSTVResult
	for h := leaderHops; h+leaderHops < len(p1200); h++ {
		if h&511 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		// Break candidate: 1200 Hz dominates with decent purity.
		if p1200[h] < 0.5 || p1200[h] < p1900[h]+0.1 {
			continue
		}
		j := h
		for j < len(p1200) && p1200[j] > 0.5 && p1200[j] > p1900[j]+0.1 {
			j++
		}
		runMs := float64(j-h) * hopSec * 1000
		if runMs < 3 || runMs > 24 {
			h = j
			continue
		}
		if !leaderHolds(p1900, p1200, h-leaderHops, h) ||
			!leaderHolds(p1900, p1200, j, j+leaderHops) {
			h = j
			continue
		}
		// The bit section starts where leader 2 collapses. Begin the
		// search near the end of the verified leader run so the 8 ms
		// window straddling the break cannot pass for the collapse;
		// the first weak hop is roughly half a window early.
		base := -1
		for k := j + leaderHops - 8; k+1 < len(p1900) && k < j+int(0.330/hopSec); k++ {
			if p1900[k] < 0.35 {
				base = k*hopN + int(0.004*float64(rate))
				break
			}
		}
		if base < 0 || base+10*bitN > len(samples) {
			h = j
			continue
		}
		// Refine the bit-section start to sample precision on the
		// demodulated trace: the start bit (1100 or 1200 Hz) is the
		// first sustained drop below 1600 Hz. The coarse hop grid can
		// place base a few ms either way, which would skew every line.
		if demod == nil {
			var err error
			demod, err = fmDemodContext(ctx, samples, rate)
			if err != nil {
				return nil, err
			}
		}
		refined := base
		lo := base - int(0.008*float64(rate))
		if lo < 0 {
			lo = 0
		}
		hi := base + int(0.008*float64(rate))
		for p := lo; p+4 < hi && p+4 < len(demod); p++ {
			if demod[p] < 1600 && demod[p+1] < 1600 && demod[p+2] < 1600 && demod[p+3] < 1600 {
				refined = p
				break
			}
		}
		base = refined
		vis, ok := readVIS(samples, rate, base, bitN)
		if !ok {
			h = j
			continue
		}
		name, known := sstvModes[vis]
		if !known {
			name = fmt.Sprintf("VIS 0x%02X", vis)
		}
		// Anchor StartSec on the refined bit-section start: the break
		// is exactly one leader (300 ms) and one break (10 ms) earlier,
		// which the coarse 2 ms hop grid cannot place precisely.
		breakStart := float64(base)/float64(rate) - 0.300 - 0.010
		res := SSTVResult{
			Mode:     name,
			VIS:      vis,
			StartSec: breakStart,
		}
		// When the clip continues well past the header, the image lines
		// must show a consistent sync cadence. A spurious header inside
		// image data is always followed by more audio, so this check
		// only fires where it has evidence to work with.
		headerEndSec := res.StartSec + 0.010 + 0.300 + 10*sstvBitLen
		if float64(len(samples))/float64(rate)-headerEndSec > 1.0 {
			if !hasSyncCadence(p1200, hopSec, headerEndSec) {
				h = j
				continue
			}
		}
		out = append(out, res)
		h = base/hopN + 150 // past the 300 ms of VIS bits
	}
	return out, nil
}

// leaderHolds reports whether every hop in [from, to) is dominated by a
// pure 1900 Hz leader tone.
func leaderHolds(p1900, p1200 []float64, from, to int) bool {
	if from < 0 || to > len(p1900) {
		return false
	}
	for k := from; k < to; k++ {
		if p1900[k] < 0.55 || p1900[k] < p1200[k] {
			return false
		}
	}
	return true
}

// hasSyncCadence reports whether the 1200 Hz purity trace after the
// header carries at least three line-sync runs at a consistent cadence
// (successive intervals within 15% of each other, in the 30-1100 ms
// range spanned by every documented SSTV mode).
func hasSyncCadence(p1200 []float64, hopSec, fromSec float64) bool {
	start := int(fromSec / hopSec)
	var runs []int // run start hops
	for i := start; i < len(p1200); {
		if p1200[i] < 0.45 {
			i++
			continue
		}
		j := i
		for j < len(p1200) && p1200[j] >= 0.45 {
			j++
		}
		if float64(j-i)*hopSec >= 0.003 { // shortest documented sync is ~4.8 ms
			runs = append(runs, i)
		}
		i = j + 1
	}
	if len(runs) < 5 {
		return false
	}
	// Sharp image edges splatter energy across the spectrum at every
	// transition, so a real frame's run list interleaves short spurious
	// runs between the true sync runs; consecutive-triple consensus
	// fails on exactly the images SSTV carries (text, test cards, bar
	// patterns). Vote on a grid instead: an anchor run plus a candidate
	// period from a consecutive spacing predicts where later syncs
	// land; random audio cannot align five runs on any grid, while a
	// real cadence aligns one per line no matter how many spurious runs
	// sit between them.
	center := make([]float64, len(runs))
	for idx, r := range runs {
		j := r
		for j < len(p1200) && p1200[j] >= 0.45 {
			j++
		}
		center[idx] = float64(r+j-1) / 2 * hopSec
	}
	periods := map[int]bool{}
	var candidates []float64
	for k := 1; k < len(center); k++ {
		d := center[k] - center[k-1]
		if d < 0.03 || d > 1.1 {
			continue
		}
		key := int(d * 1000)
		if !periods[key] {
			periods[key] = true
			candidates = append(candidates, d)
		}
	}
	for _, d := range candidates {
		tol := d * 0.12
		if tol < 0.012 {
			tol = 0.012
		}
		for a, anchor := range center {
			aligned := 1
			for b := a + 1; b < len(center); b++ {
				k := (center[b] - anchor) / d
				nearest := float64(int(k + 0.5))
				if nearest < 1 {
					continue
				}
				if diff := center[b] - anchor - nearest*d; diff < tol && diff > -tol {
					aligned++
				}
			}
			if aligned >= 5 {
				return true
			}
		}
	}
	return false
}

// readVIS reads a start bit, seven data bits LSB first, an even parity
// bit and a 1200 Hz stop bit starting at sample offset base, and returns
// the 7-bit VIS code. The start bit may be 1200 Hz (original spec and
// pysstv) or 1100 Hz (some encoders); the stop bit must be 1200 Hz.
func readVIS(samples []float64, rate, base, bitSamples int) (int, bool) {
	if bitSamples < 8 {
		return 0, false
	}
	// Measure each bit over its middle two thirds so a couple of
	// milliseconds of base misalignment cannot straddle bit boundaries.
	span := bitSamples * 2 / 3
	off := (bitSamples - span) / 2
	readBit := func(i int) (g1100, g1200, g1300 float64, ok bool) {
		from := base + i*bitSamples + off
		to := from + span
		if to > len(samples) {
			return 0, 0, 0, false
		}
		seg := samples[from:to]
		return goertzel(seg, rate, sstvBit1),
			goertzel(seg, rate, sstvBreak),
			goertzel(seg, rate, sstvBit0), true
	}
	g1100, g1200, g1300, ok := readBit(0)
	if !ok || math.Max(g1100, g1200) <= g1300*1.5 {
		return 0, false // start bit must be clearly below 1300 Hz
	}
	vis := 0
	for i := 0; i < 7; i++ {
		g1, _, g0, ok := readBit(i + 1)
		if !ok {
			return 0, false
		}
		if math.Max(g1, g0) < math.Min(g1, g0)*1.5 {
			return 0, false // ambiguous bit, not clean FSK
		}
		if g1 > g0 {
			vis |= 1 << i
		}
	}
	g1100p, _, g1300p, ok := readBit(8)
	if !ok {
		return 0, false
	}
	parity := 0
	if g1100p > g1300p {
		parity = 1
	}
	ones := 0
	for i := 0; i < 7; i++ {
		ones += (vis >> i) & 1
	}
	if (ones+parity)%2 != 0 {
		return 0, false
	}
	g1100s, g1200s, g1300s, ok := readBit(9)
	if !ok || g1200s <= g1300s || g1200s <= g1100s {
		return 0, false // stop bit must be 1200 Hz
	}
	return vis, true
}
