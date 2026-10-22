package decode

import (
	"fmt"
	"math"
	"strings"
)

// fskDemod recovers NRZI-decoded bits from Bell 202 AFSK audio (1200 Hz
// mark / 2200 Hz space), following the structure of Sailer's demod_afsk12
// (multimon-ng): correlators of exactly one bit period against both
// tones, subsampled decisions, and a proportional PLL on tone
// transitions. Input is resampled to the reference rate of 22050 Hz
// because the correlator length and PLL increment are tuned for it.
func fskDemod(samples []float64, rate int) []bool {
	return fskDemodOpt(samples, rate, fskOpts{markGain: 1, pllDiv: 8})
}

// fskOpts tunes one demodulator variant. markGain scales the 2200 Hz
// correlator energy against the 1200 Hz one to compensate for pre- or
// de-emphasised audio; pllDiv sets how hard the bit clock follows tone
// transitions (smaller is faster); offset delays the first sample so
// variants sample the bit period at different phases.
type fskOpts struct {
	markGain float64
	pllDiv   uint32
	offset   int
}

func fskDemodOpt(samples []float64, rate int, o fskOpts) []bool {
	// Resample to 22050 Hz: the correlation window must be exactly one bit
	// period and the PLL increment is fixed-point, so the reference rate is
	// built in. Linear interpolation is sufficient; the correlator
	// tolerates the interpolation noise.
	const refRate = 22050
	x := samples
	if rate != refRate {
		x = resampleLinear(samples, rate, refRate)
	}
	if o.offset > 0 && o.offset < len(x) {
		x = x[o.offset:]
	}
	n := len(x)
	const corrLen = refRate / 1200 // 18 samples, one bit period
	const subSamp = 2
	// Fixed-point phase: 0x10000 = one bit period.
	const sphaseInc = 0x10000 * 1200 * subSamp / refRate

	// Correlator tables. Bell 202 / AX.25: mark = 2200 Hz, space = 1200 Hz
	// (multimon-ng demod_afsk12.c). f = mark - space, so f > 0 means mark.
	var corrMI, corrMQ, corrSI, corrSQ [corrLen]float64
	for i := 0; i < corrLen; i++ {
		wM := 2 * math.Pi * 2200 * float64(i) / refRate
		wS := 2 * math.Pi * 1200 * float64(i) / refRate
		corrMI[i], corrMQ[i] = math.Cos(wM), math.Sin(wM)
		corrSI[i], corrSQ[i] = math.Cos(wS), math.Sin(wS)
	}

	var bits []bool
	var shreg uint32
	var sphase uint32
	var lasts uint8
	for i := 0; i+corrLen <= n; i += subSamp {
		var mi, mq, si, sq float64
		for j := 0; j < corrLen; j++ {
			s := x[i+j]
			mi += s * corrMI[j]
			mq += s * corrMQ[j]
			si += s * corrSI[j]
			sq += s * corrSQ[j]
		}
		f := o.markGain*(mi*mi+mq*mq) - (si*si + sq*sq)
		shreg = (shreg << 1) | uint32(boolToInt(f > 0))
		// Proportional PLL on tone transitions. The parentheses matter:
		// the transition flag is bit 0 of the XOR, not of the whole shift.
		if (shreg^(shreg>>1))&1 == 1 {
			if sphase < 0x8000-sphaseInc/2 {
				sphase += sphaseInc / o.pllDiv
			} else {
				sphase -= sphaseInc / o.pllDiv
			}
		}
		sphase += sphaseInc
		if sphase >= 0x10000 {
			sphase &= 0xFFFF
			lasts = (lasts << 1) | uint8(shreg&1)
			// NRZI decode: transition = 0 bit, held tone = 1.
			curbit := (lasts ^ (lasts >> 1) ^ 1) & 1
			bits = append(bits, curbit == 1)
		}
	}
	return bits
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// resampleLinear converts mono samples to a new rate with linear
// interpolation.
func resampleLinear(samples []float64, from, to int) []float64 {
	if from == to || len(samples) == 0 {
		return samples
	}
	ratio := float64(from) / float64(to)
	outLen := int(float64(len(samples)) / ratio)
	out := make([]float64, outLen)
	for i := 0; i < outLen; i++ {
		pos := float64(i) * ratio
		i0 := int(pos)
		if i0 >= len(samples)-1 {
			out[i] = samples[len(samples)-1]
			continue
		}
		frac := pos - float64(i0)
		out[i] = samples[i0]*(1-frac) + samples[i0+1]*frac
	}
	return out
}

// hdlcFrames scans a post-NRZI bit stream for AX.25 HDLC frames. It finds
// 0x7E flag boundaries, de-stuffs the data between them (a 0 after five
// 1s), and keeps only frames whose CCITT CRC-16 checks out.
func hdlcFrames(bits []bool) [][]byte {
	var frames [][]byte
	var win uint16
	var data []bool
	seenFlag := false

	flush := func() {
		if len(data) < 24 { // shorter than address+control+CRC is not a frame
			data = data[:0]
			return
		}
		f := destuff(data)
		// A frame carrying a valid AX.25 FCS leaves the standard residue
		// 0xF0B8 after the CRC is run over the whole frame including the
		// transmitted (complemented) FCS.
		if f != nil && len(f) >= 3 && crc16(f) == 0xF0B8 {
			frames = append(frames, f)
		}
		data = data[:0]
	}

	for _, bit := range bits {
		win = ((win << 1) | uint16(bitValue(bit))) & 0xFF
		if win == 0x7E {
			if seenFlag {
				// The 7 leading bits of this flag were already appended;
				// drop them so only the data between flags remains.
				if len(data) >= 7 {
					data = data[:len(data)-7]
				} else {
					data = data[:0]
				}
				flush()
			}
			seenFlag = true
			continue
		}
		if seenFlag {
			data = append(data, bit)
		}
	}
	return frames
}

// destuff removes HDLC bit stuffing and packs the result into bytes,
// least-significant bit first. It returns nil if the bits do not form a
// whole number of bytes.
func destuff(bits []bool) []byte {
	var out []byte
	var cur byte
	var n, ones int
	for _, bit := range bits {
		if bit {
			ones++
		} else {
			if ones == 5 {
				ones = 0
				continue // stuffing bit, not data
			}
			ones = 0
		}
		cur >>= 1
		if bit {
			cur |= 0x80
		}
		n++
		if n == 8 {
			out = append(out, cur)
			cur, n = 0, 0
		}
	}
	if n != 0 {
		return nil
	}
	return out
}

func bitValue(b bool) byte {
	if b {
		return 1
	}
	return 0
}

// crc16 is the CCITT CRC used by AX.25 (reflected polynomial 0x8408,
// init 0xFFFF, no final XOR): a frame's CRC is chosen so this reads 0.
func crc16(data []byte) uint16 {
	crc := uint16(0xFFFF)
	for _, b := range data {
		crc ^= uint16(b)
		for i := 0; i < 8; i++ {
			if crc&1 != 0 {
				crc = (crc >> 1) ^ 0x8408
			} else {
				crc >>= 1
			}
		}
	}
	return crc
}

// AX25 is one decoded packet: addresses, protocol bytes and the info text.
type AX25 struct {
	Destination string
	Source      string
	Repeaters   []string
	Control     byte
	PID         byte
	Info        string
}

// Address renders one AX.25 callsign with its SSID.
func (f AX25) String() string {
	s := fmt.Sprintf("%s>%s", f.Source, f.Destination)
	for _, r := range f.Repeaters {
		s += "," + r
	}
	if len(f.Info) > 0 {
		s += ": " + f.Info
	}
	return s
}

// parseAX25 decodes the fixed AX.25 header. Addresses are six shifted
// ASCII characters followed by an SSID byte; the low bit of the SSID byte
// marks the last address in the chain.
func parseAX25(frame []byte) (AX25, bool) {
	if len(frame) < 15 {
		return AX25{}, false
	}
	call := func(i int) (string, byte) {
		var sb strings.Builder
		for _, c := range frame[i : i+6] {
			sb.WriteByte(c >> 1)
		}
		name := strings.TrimSpace(sb.String())
		return name, frame[i+6]
	}
	dest, destSSID := call(0)
	src, srcSSID := call(7)
	out := AX25{
		Destination: fmt.Sprintf("%s-%d", dest, destSSID>>1&0xF),
		Source:      fmt.Sprintf("%s-%d", src, srcSSID>>1&0xF),
	}
	// Walk any repeater addresses until the SSID extension bit is set.
	i := 7
	for srcSSID&1 == 0 && i+7+7 <= len(frame) {
		r, ssid := call(i + 7)
		out.Repeaters = append(out.Repeaters, fmt.Sprintf("%s-%d", r, ssid>>1&0xF))
		i += 7
		srcSSID = ssid
	}
	rest := frame[i+7:]
	if len(rest) < 3 {
		return out, false
	}
	out.Control = rest[0]
	out.PID = rest[1]
	info := rest[2:]
	// Drop the two FCS bytes that close the frame.
	if len(info) >= 2 {
		info = info[:len(info)-2]
	}
	out.Info = printable(info)
	return out, true
}

func printable(b []byte) string {
	var sb strings.Builder
	for _, c := range b {
		if c >= 0x20 && c < 0x7F {
			sb.WriteByte(c)
		} else {
			sb.WriteByte(' ')
		}
	}
	return strings.TrimSpace(sb.String())
}

// AFSKFrames runs the whole chain on one WAV file and returns decoded
// packets. A power gate skips stretches of silence so the demodulator is
// not asked to find bits in noise.
func AFSKFrames(path string) ([]AX25, error) {
	samples, rate, err := ReadWAV(path)
	if err != nil {
		return nil, err
	}
	if rate < 4800 {
		return nil, fmt.Errorf("%s: sample rate %d Hz too low for AFSK 1200", path, rate)
	}
	bursts := findBursts(samples, rate, rate/10)
	if len(bursts) == 0 {
		// No clear power bursts: demodulate the whole file rather than
		// give up. The HDLC CRC check discards anything that is noise.
		bursts = [][2]int{{0, len(samples)}}
	}
	var out []AX25
	for _, b := range bursts {
		for _, f := range ensembleFrames(samples[b[0]:b[1]], rate) {
			if ax, ok := parseAX25(f); ok {
				out = append(out, ax)
			}
		}
	}
	return out, nil
}

// findBursts returns [start, end) sample index pairs where the signal
// power is meaningfully above the file's median level, with a little
// padding either side so packet edges survive the burst boundary.
func findBursts(samples []float64, rate, minLen int) [][2]int {
	power := squelchPower(samples, rate)
	if len(power) == 0 {
		return nil
	}
	// Median power as the noise floor reference.
	sorted := make([]float64, len(power))
	copy(sorted, power)
	// Cheaper than a full sort: subsample the power array.
	const stride = 97
	var levels []float64
	for i := 0; i < len(sorted); i += stride {
		levels = append(levels, sorted[i])
	}
	sortFloats(levels)
	if len(levels) == 0 {
		return nil
	}
	floor := levels[len(levels)/2]
	threshold := floor*3 + 1e-9

	var bursts [][2]int
	var start = -1
	gap := rate / 10
	lastAbove := -1
	for i, p := range power {
		if p > threshold {
			if start < 0 {
				start = i
			}
			lastAbove = i
		} else if start >= 0 && i-lastAbove > gap {
			if lastAbove-start >= minLen {
				bursts = append(bursts, [2]int{start, lastAbove + 1})
			}
			start = -1
		}
	}
	if start >= 0 && lastAbove-start >= minLen {
		bursts = append(bursts, [2]int{start, lastAbove + 1})
	}
	// Pad and clip.
	for i := range bursts {
		bursts[i][0] -= rate / 4
		if bursts[i][0] < 0 {
			bursts[i][0] = 0
		}
		bursts[i][1] += rate / 4
		if bursts[i][1] > len(samples) {
			bursts[i][1] = len(samples)
		}
	}
	return bursts
}

func sortFloats(v []float64) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j] < v[j-1]; j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}

var _ = math.Pi

// ensembleVariants are the demodulator settings tried on every burst. Real
// off-air audio is pre- or de-emphasised by an unknown amount, so one
// fixed tone balance misses a third of the packets; running several and
// keeping every distinct CRC-valid frame recovers them (the same idea as
// Dire Wolf's multiple decoders). The CRC makes false positives negligible.
func ensembleVariants() []fskOpts {
	var v []fskOpts
	for _, g := range []float64{1, 0.5, 2, 0.25, 4} {
		for _, p := range []uint32{8, 4} {
			v = append(v, fskOpts{markGain: g, pllDiv: p})
		}
	}
	v = append(v, fskOpts{markGain: 1, pllDiv: 8, offset: 5}, fskOpts{markGain: 1, pllDiv: 8, offset: 11})
	return v
}

// ensembleFrames runs every variant and returns the union of CRC-valid
// frames. A frame seen n times by one variant counts n times in the
// result, so identical back-to-back transmissions are not collapsed.
func ensembleFrames(samples []float64, rate int) [][]byte {
	best := map[string]int{}
	first := map[string][]byte{}
	var order []string
	for _, o := range ensembleVariants() {
		seen := map[string]int{}
		for _, f := range hdlcFrames(fskDemodOpt(samples, rate, o)) {
			k := string(f)
			seen[k]++
			if _, ok := first[k]; !ok {
				first[k] = f
				order = append(order, k)
			}
		}
		for k, n := range seen {
			if n > best[k] {
				best[k] = n
			}
		}
	}
	var out [][]byte
	for _, k := range order {
		for i := 0; i < best[k]; i++ {
			out = append(out, first[k])
		}
	}
	return out
}
