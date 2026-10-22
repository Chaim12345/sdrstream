package decode

import (
	"encoding/binary"
	"fmt"
	"math"
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

// acarsMSK is a coherent MSK demodulator for 12.5 kHz audio: the input is
// mixed with a 1800 Hz PLL oscillator, a half-wave cosine matched filter
// (oversampled 12x) is applied once per bit, and the I and Q rails
// alternate bit by bit. The structure follows the acarsdec design by
// Thierry Leconte as reimplemented in sdr-acars (jasonherald, LGPL-2.0).
type acarsMSK struct {
	phi, clk, df float64
	s            uint32
	inb          [acarsFLEN]complex128
	idx          int
	h            [acarsFLEN*acarsOver + 1]float64
}

const (
	acarsRate    = 12500
	acarsFLEN    = acarsRate/1200 + 1
	acarsOver    = 12
	acarsPLLGain = 38e-4
	acarsPLLCoef = 0.52
)

func newACARSMSK() *acarsMSK {
	m := &acarsMSK{}
	c := (len(m.h) - 1) / 2
	for i := range m.h {
		n := float64(i - c)
		v := math.Cos(2 * math.Pi * 600 / acarsRate / acarsOver * n)
		if v < 0 {
			v = 0
		}
		m.h[i] = v
	}
	return m
}

// bit feeds one demodulated bit decision to the frame parser; it returns
// whether the parser asks for a polarity flip.
func (m *acarsMSK) run(x []float64, put func(v float64) bool) {
	for _, in := range x {
		sp := 1800.0/acarsRate*2*math.Pi + m.df
		m.phi += sp
		if m.phi >= 2*math.Pi {
			m.phi -= 2 * math.Pi
		}
		m.inb[m.idx] = complex(in*math.Cos(m.phi), -in*math.Sin(m.phi))
		m.idx = (m.idx + 1) % acarsFLEN
		m.clk += sp
		thr := 3*math.Pi/2 - sp/2
		if m.clk < thr {
			continue
		}
		m.clk -= 3 * math.Pi / 2
		o := int(acarsOver * (m.clk/sp + 0.5))
		if o > acarsOver {
			o = acarsOver
		}
		if o < 0 {
			o = 0
		}
		var v complex128
		for j := 0; j < acarsFLEN; j++ {
			v += m.inb[(j+m.idx)%acarsFLEN] * complex(m.h[o], 0)
			o += acarsOver
		}
		lvl := math.Hypot(real(v), imag(v))
		v /= complex(lvl+1e-8, 0)
		var vo, dphi float64
		if m.s&1 != 0 {
			vo = imag(v)
			if vo >= 0 {
				dphi = -real(v)
			} else {
				dphi = real(v)
			}
		} else {
			vo = real(v)
			if vo >= 0 {
				dphi = imag(v)
			} else {
				dphi = -imag(v)
			}
		}
		if m.s&2 != 0 {
			vo = -vo
		}
		if put(vo) {
			m.s ^= 2
		}
		m.s++
		m.df = acarsPLLCoef*m.df + (1-acarsPLLCoef)*acarsPLLGain*dphi
	}
}

// acarsFramer turns the bit stream into blocks.
type acarsFramer struct {
	state    int // 0 syn1, 1 syn2, 2 soh, 3 text, 4 crc1, 5 crc2
	out      byte
	nbits    int
	buf      []byte
	crcB     [2]byte
	flip     bool
	perr     int
	emit     func(buf []byte)
	bitsSeen int
}

func newACARSFramer(emit func([]byte)) *acarsFramer {
	return &acarsFramer{nbits: 8, emit: emit}
}

func (f *acarsFramer) idle() {
	f.state = 0
	f.nbits = 1
	f.buf = f.buf[:0]
	f.perr = 0
}

func oddParity(b byte) bool { return bitsOnes(b)&1 == 1 }

func bitsOnes(b byte) int {
	n := 0
	for ; b != 0; b &= b - 1 {
		n++
	}
	return n
}

func (f *acarsFramer) put(v float64) bool {
	f.out >>= 1
	if v > 0 {
		f.out |= 0x80
	}
	f.nbits--
	if f.nbits > 0 {
		return false
	}
	b := f.out
	f.byteIn(b)
	fl := f.flip
	f.flip = false
	return fl
}

func (f *acarsFramer) byteIn(b byte) {
	const syn, soh, etx, etb = 0x16, 0x01, 0x83, 0x97
	switch f.state {
	case 0:
		if b == syn {
			f.state, f.nbits = 1, 8
		} else if b == ^byte(syn) {
			f.flip = true
			f.state, f.nbits = 1, 8
		} else {
			f.nbits = 1
		}
	case 1:
		if b == syn {
			f.state, f.nbits = 2, 8
		} else if b == ^byte(syn) {
			f.flip = true
			f.nbits = 8
		} else {
			f.idle()
		}
	case 2:
		if b == soh {
			f.buf = f.buf[:0]
			f.perr = 0
			f.state, f.nbits = 3, 8
		} else {
			f.idle()
		}
	case 3:
		f.buf = append(f.buf, b)
		if !oddParity(b) {
			f.perr++
			if f.perr > 3 {
				f.idle()
				return
			}
		}
		if b == etx || b == etb {
			f.state, f.nbits = 4, 8
			return
		}
		if len(f.buf) > 240 {
			f.idle()
			return
		}
		f.nbits = 8
	case 4:
		f.crcB[0] = b
		f.state, f.nbits = 5, 8
	case 5:
		f.crcB[1] = b
		var crc uint16
		for _, c := range f.buf {
			crc = acarsCRCUpdate(crc, c)
		}
		crc = acarsCRCUpdate(crc, f.crcB[0])
		crc = acarsCRCUpdate(crc, f.crcB[1])
		if crc == 0 && f.perr == 0 && len(f.buf) >= 13 {
			f.emit(append([]byte(nil), f.buf...))
		}
		f.idle()
	}
}

// ACARSDecode decodes ACARS blocks from one channel of AM-demodulated
// audio. Only blocks with odd parity on every character and a valid CRC
// are returned.
func ACARSDecode(samples []float64, rate int, channel int) []ACARSMsg {
	x := samples
	if rate != acarsRate {
		x = resampleLinear(samples, rate, acarsRate)
	}
	// Peak-normalise so the matched filter sees a sane level.
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
	fr := newACARSFramer(func(buf []byte) {
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
	})
	msk := newACARSMSK()
	// position tracking for AtSec: approximate by sample index per chunk
	const chunk = 125
	for i := 0; i < len(x); i += chunk {
		at = i
		e := i + chunk
		if e > len(x) {
			e = len(x)
		}
		msk.run(x[i:e], fr.put)
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
