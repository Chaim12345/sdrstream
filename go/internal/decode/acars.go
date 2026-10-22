package decode

import (
	"encoding/binary"
	"fmt"
	"math"
	"math/bits"
	"os"
	"strings"
)

// ACARS (ARINC 618) air-ground datalink, VHF AM around 131.5 MHz.
//
// After AM demodulation the audio is a 2400 baud MSK signal whose two
// tones are 1200 Hz and 2400 Hz. Bytes are 7 data bits plus an odd parity
// bit, sent LSB first. A message is
//
//	prekey, SYN SYN, SOH, mode, address(7), ack, label(2), block id,
//	[STX text], ETX|ETB, BCS(2), DEL
//
// where BCS is a CRC-16 (Kermit, reflected 0x8408) over the bytes after SOH
// through the suffix and BCS. The gate: odd parity on every character
// from SOH to the suffix, and the CRC. A frame that fails either is
// dropped, so noise cannot produce output.

// ACARSMsg is one parity- and CRC-valid ACARS block.
type ACARSMsg struct {
	Mode    byte
	Reg     string // aircraft registration, leading dots removed
	Ack     byte
	Label   string
	BlockID byte
	Text    string
	Suffix  byte // 0x03 ETX or 0x17 ETB
	Channel int
	AtSec   float64
}

func (m ACARSMsg) String() string {
	return fmt.Sprintf("mode=%c reg=%s label=%s blk=%c ack=%c text=%q", m.Mode, m.Reg, strings.TrimSpace(m.Label), printableByte(m.BlockID), printableByte(m.Ack), m.Text)
}

func printableByte(b byte) byte {
	if b < 0x20 || b > 0x7e {
		return '?'
	}
	return b
}

func acarsCRCUpdate(crc uint16, c byte) uint16 {
	crc ^= uint16(c)
	for i := 0; i < 8; i++ {
		if crc&1 == 1 {
			crc = crc>>1 ^ 0x8408
		} else {
			crc >>= 1
		}
	}
	return crc
}

// acarsRate is the audio rate the demodulator works at. Other input rates
// are resampled to it.
const acarsRate = 12500

const (
	acarsBaud   = 2400.0
	acarsCenter = 1800.0 // midpoint of the 1200 and 2400 Hz tones
	acarsLPTaps = 33

	// Demodulator tuning, found on the recordings in the test suite.
	acarsLPCut       = 1200.0 // baseband low-pass cutoff, Hz
	acarsEdgeThresh  = 0.08   // |rad/sample| a tone estimate must reach to count
	acarsTimingOff   = 0.45   // bit phase at which the frequency estimate crosses zero
	acarsTimingGain  = 0.1
	acarsCostasAlpha = 0.1
	acarsCostasBeta  = 0.005

	acarsMaxBlk  = 270 // longest block body we accept, in characters
	acarsSyn     = 0x16
	acarsSOH     = 0x01
	acarsETX     = 0x03
	acarsETB     = 0x17
	acarsMinBody = 13 // mode, 7 address, ack, 2 label, block id, suffix
)

// acarsDemod is a coherent MSK demodulator. The audio is mixed down by the
// midpoint of the two tones (1800 Hz) to a complex baseband and low passed.
// With tones at +-600 Hz around that midpoint, the baseband phase moves by
// +-90 degrees over every bit, so at bit boundaries it sits on a multiple of
// 90 degrees. Rotating boundary sample n by (-j)^n turns the alternating
// in-phase/quadrature rails into one real BPSK stream, which a small Costas
// loop locks and slices. Bit timing comes from a loop that follows the
// instantaneous-frequency zero crossings (tone changes happen on bit edges).
type acarsDemod struct {
	taps         []float64
	ringI, ringQ []float64
	pos          int
	osc          float64
	prevI, prevQ float64
	havePrev     bool

	spb  float64 // samples per bit
	ph   float64 // bit phase in [0,1); bit edges at 0
	last float64 // previous smoothed frequency estimate
	high bool    // tone side of the last clear frequency estimate
	cnt  int     // boundary counter, drives the (-j)^n rotation

	psi, omega float64 // Costas phase and frequency
}

func newACARSDemod() *acarsDemod {
	d := &acarsDemod{spb: float64(acarsRate) / acarsBaud}
	d.taps = make([]float64, acarsLPTaps)
	mid := float64(acarsLPTaps-1) / 2
	fc := acarsLPCut / float64(acarsRate)
	var sum float64
	for i := range d.taps {
		t := float64(i) - mid
		s := 2 * fc
		if t != 0 {
			s = math.Sin(2*math.Pi*fc*t) / (math.Pi * t)
		}
		w := 0.54 - 0.46*math.Cos(2*math.Pi*float64(i)/float64(acarsLPTaps-1))
		d.taps[i] = s * w
		sum += d.taps[i]
	}
	for i := range d.taps {
		d.taps[i] /= sum
	}
	d.ringI = make([]float64, acarsLPTaps)
	d.ringQ = make([]float64, acarsLPTaps)
	return d
}

// run consumes audio and calls out(bit) once per bit period.
func (d *acarsDemod) run(x []float64, out func(bit bool)) {
	w := 2 * math.Pi * acarsCenter / float64(acarsRate)
	for _, v := range x {
		d.osc += w
		if d.osc > 2*math.Pi {
			d.osc -= 2 * math.Pi
		}
		d.ringI[d.pos] = v * math.Cos(d.osc)
		d.ringQ[d.pos] = -v * math.Sin(d.osc)
		var si, sq float64
		k := d.pos
		for _, t := range d.taps {
			si += t * d.ringI[k]
			sq += t * d.ringQ[k]
			if k == 0 {
				k = acarsLPTaps
			}
			k--
		}
		d.pos++
		if d.pos == acarsLPTaps {
			d.pos = 0
		}
		if !d.havePrev {
			d.prevI, d.prevQ, d.havePrev = si, sq, true
			continue
		}
		// Phase step between consecutive samples: arg(z[n] * conj(z[n-1])).
		re := si*d.prevI + sq*d.prevQ
		im := sq*d.prevI - si*d.prevQ
		d.prevI, d.prevQ = si, sq
		d.step(si, sq, math.Atan2(im, re), out)
	}
}

func (d *acarsDemod) step(si, sq, f float64, out func(bool)) {
	// A tone change is a crossing of the frequency estimate through zero
	// between two clearly signed values; small wobbles around zero are
	// ignored.
	s := 0.5*f + 0.5*d.last
	d.last = s
	if s > acarsEdgeThresh || s < -acarsEdgeThresh {
		if (s > 0) != d.high {
			d.high = s > 0
			e := d.ph - acarsTimingOff
			if e > 0.5 {
				e -= 1
			} else if e < -0.5 {
				e += 1
			}
			d.ph -= acarsTimingGain * e
		}
	}
	d.ph += 1 / d.spb
	if d.ph < 1 {
		return
	}
	d.ph -= 1
	// Bit boundary sample: rotate by (-j)^n, then by the Costas phase.
	zi, zq := si, sq
	switch d.cnt & 3 {
	case 1: // times -j
		zi, zq = sq, -si
	case 2:
		zi, zq = -si, -sq
	case 3: // times +j
		zi, zq = -sq, si
	}
	d.cnt++
	c, sn := math.Cos(d.psi), math.Sin(d.psi)
	vi := zi*c + zq*sn
	vq := zq*c - zi*sn
	if m := vi*vi + vq*vq; m > 1e-18 {
		e := vi * vq / m
		d.omega += acarsCostasBeta * e
		d.psi += acarsCostasAlpha*e + d.omega
	}
	out(vi > 0)
}

// acarsFramer finds the SYN SYN preamble in a bit stream, assembles
// characters (LSB first, 7 data bits plus odd parity) and validates the
// block. The coherent stream can come out inverted, and with the rail
// alternation resolved either way, so four preamble variants are hunted.
type acarsFramer struct {
	emit   func([]byte)
	shiftA uint16 // raw bits, newest in the top bit
	shiftB uint16 // bits XORed with the bit-index parity
	idx    int
	hunt   bool
	alt    bool
	inv    bool
	nbit   int
	cur    byte
	buf    []byte
	sawSOH bool
}

func newACARSFramer(emit func([]byte)) *acarsFramer {
	return &acarsFramer{emit: emit, hunt: true}
}

func oddParity(b byte) bool { return bitsOnes(b)&1 == 1 }

func bitsOnes(b byte) int {
	n := 0
	for ; b != 0; b &= b - 1 {
		n++
	}
	return n
}

func (f *acarsFramer) put(bit bool) {
	b := uint16(0)
	if bit {
		b = 1
	}
	par := uint16(f.idx & 1)
	f.idx++
	if f.hunt {
		f.shiftA = f.shiftA>>1 | b<<15
		f.shiftB = f.shiftB>>1 | (b^par)<<15
		const syn = acarsSyn | acarsSyn<<8
		// Allow one wrong bit in the preamble; the parity, suffix and
		// CRC checks reject anything that is not a real block.
		switch {
		case bits.OnesCount16(f.shiftA^syn) <= 1:
			f.start(false, false)
		case bits.OnesCount16(f.shiftA^^uint16(syn)) <= 1:
			f.start(false, true)
		case bits.OnesCount16(f.shiftB^syn) <= 1:
			f.start(true, false)
		case bits.OnesCount16(f.shiftB^^uint16(syn)) <= 1:
			f.start(true, true)
		}
		return
	}
	if f.alt {
		b ^= par
	}
	if f.inv {
		b ^= 1
	}
	f.cur |= byte(b) << f.nbit
	f.nbit++
	if f.nbit < 8 {
		return
	}
	c := f.cur
	f.nbit, f.cur = 0, 0
	if !f.sawSOH {
		switch {
		case c == acarsSOH:
			f.sawSOH = true
		case c == acarsSyn: // extra sync characters
		default:
			f.stop()
		}
		return
	}
	f.buf = append(f.buf, c)
	n := len(f.buf)
	if n > acarsMaxBlk {
		f.stop()
		return
	}
	// The block is complete once the character three back is ETX/ETB and
	// the two characters after it satisfy the block check. The trailing DEL
	// is not needed, so a damaged DEL does not lose the block.
	if n >= acarsMinBody+2 {
		if s := f.buf[n-3] & 0x7f; s == acarsETX || s == acarsETB {
			if f.finish() {
				f.stop()
			}
		}
	}
}

// start switches from hunting to collecting. The preamble just seen is two
// whole characters, so the next bit begins a new character.
func (f *acarsFramer) start(alt, inv bool) {
	f.hunt, f.alt, f.inv = false, alt, inv
	f.nbit, f.cur, f.buf, f.sawSOH = 0, 0, f.buf[:0], false
}

func (f *acarsFramer) stop() { f.hunt, f.shiftA, f.shiftB = true, 0, 0 }

// finish checks parity and the block check on the collected characters,
// which end with the suffix and the two BCS characters. It reports whether
// a valid block was emitted.
func (f *acarsFramer) finish() bool {
	b := f.buf
	n := len(b)
	for _, c := range b[:n-2] {
		if !oddParity(c) {
			return false
		}
	}
	var crc uint16
	for _, c := range b {
		crc = acarsCRCUpdate(crc, c)
	}
	if crc != 0 {
		return false
	}
	f.emit(append([]byte(nil), b[:n-2]...))
	return true
}

// ACARSDecode decodes ACARS blocks from one channel of AM-demodulated
// audio. Only blocks with odd parity on every character and a valid CRC
// are returned.
func ACARSDecode(samples []float64, rate int, channel int) []ACARSMsg {
	x := samples
	if rate != acarsRate {
		x = resampleLinear(samples, rate, acarsRate)
	}
	var peak float64
	for _, v := range x {
		peak = math.Max(peak, math.Abs(v))
	}
	if peak > 0 {
		y := make([]float64, len(x))
		for i, v := range x {
			y[i] = v / peak
		}
		x = y
	}
	var out []ACARSMsg
	var at int
	emit := func(buf []byte) {
		body := buf[:len(buf)-1]
		suffix := buf[len(buf)-1] & 0x7f
		m := ACARSMsg{Mode: body[0] & 0x7f, Ack: acarsAck(body[8] & 0x7f), Label: acarsLabel(body[9]&0x7f, body[10]&0x7f), BlockID: body[11] & 0x7f, Suffix: suffix, Channel: channel, AtSec: float64(at) / acarsRate}
		reg := make([]byte, 7)
		for i := range reg {
			reg[i] = body[1+i] & 0x7f
		}
		m.Reg = strings.TrimLeft(string(reg), ".")
		var sb strings.Builder
		for _, c := range body[12:] {
			c &= 0x7f
			if c == 0x02 {
				continue
			}
			if c == '\r' || c == '\n' || (c >= 0x20 && c < 0x7f) {
				sb.WriteByte(c)
			}
		}
		m.Text = sb.String()
		out = append(out, m)
	}
	fr := newACARSFramer(emit)
	dm := newACARSDemod()
	const chunk = 125
	for i := 0; i < len(x); i += chunk {
		at = i
		e := i + chunk
		if e > len(x) {
			e = len(x)
		}
		dm.run(x[i:e], fr.put)
	}
	return out
}

// ReadWAVChannels loads a 16-bit PCM WAV and returns each channel
// separately.
func ReadWAVChannels(path string) ([][]float64, int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, err
	}
	if len(data) < 44 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return nil, 0, fmt.Errorf("%s: not a RIFF/WAVE file", path)
	}
	rate := int(binary.LittleEndian.Uint32(data[24:28]))
	ch := int(binary.LittleEndian.Uint16(data[22:24]))
	if ch < 1 || binary.LittleEndian.Uint16(data[34:36]) != 16 {
		return nil, 0, fmt.Errorf("%s: only 16-bit PCM is supported", path)
	}
	if rate <= 0 {
		return nil, 0, fmt.Errorf("%s: bad sample rate %d", path, rate)
	}
	off := 12
	for off+8 <= len(data) {
		id := string(data[off : off+4])
		size := int(binary.LittleEndian.Uint32(data[off+4 : off+8]))
		body := off + 8
		if id == "data" {
			end := body + size
			if end > len(data) {
				end = len(data)
			}
			n := (end - body) / 2 / ch
			out := make([][]float64, ch)
			for c := range out {
				out[c] = make([]float64, n)
			}
			for i := 0; i < n; i++ {
				for c := 0; c < ch; c++ {
					out[c][i] = float64(int16(binary.LittleEndian.Uint16(data[body+(i*ch+c)*2:]))) / 32768
				}
			}
			return out, rate, nil
		}
		off = body + size + size%2
	}
	return nil, 0, fmt.Errorf("%s: no data chunk", path)
}

func acarsLabel(a, b byte) string {
	if b == 0x7f {
		b = 'd'
	}
	return string([]byte{a, b})
}

func acarsAck(a byte) byte {
	if a == 0x15 {
		return '!' // NAK
	}
	return a
}
