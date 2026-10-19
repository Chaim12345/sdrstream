// Package vocoder decodes the WebSDR audio stream.
//
// WebSDR does not send PCM. It sends a compact stream that mixes two sample
// sources:
//
//   - 0x80 frames: 128 samples looked up from a 256-entry companding table.
//   - LPC frames: 128 samples reconstructed by a 20th-order LPC synthesis
//     filter driven by an adaptive quantiser.
//
// Everything else in the stream is metadata: sample rate, filter coefficients,
// AGC state, tuned frequency, S-meter, timestamps.
//
// This is a port of the decoder from the public WebSDR HTML5 client
// (websdr-sound.js, copyright 2013-2025 PA3FWM). JavaScript's <<, >> and |
// operators coerce to 32-bit signed integers, and the reference code depends
// on that behaviour for quantiser overflow, so those semantics are reproduced
// explicitly here.
package vocoder

import "math"

// quantizerZ holds the magnitude thresholds, indexed by G.
var quantizerZ = [8]int{999, 999, 8, 4, 2, 1, 99, 99}

// toInt32 coerces a float to int32 using JavaScript ToInt32 semantics:
// truncate toward zero, then take the value modulo 2^32.
func toInt32(value float64) int32 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	truncated := math.Trunc(value)
	remainder := math.Mod(truncated, 4294967296.0)
	if remainder < 0 {
		remainder += 4294967296.0
	}
	return int32(uint32(remainder))
}

// i16 coerces to a signed 16-bit integer, as Int16Array assignment does.
func i16(value float64) int16 {
	return int16(uint16(int64(math.Trunc(value))))
}

// Vocoder is a stateful decoder for WebSDR stream messages. Feed each
// WebSocket binary message to Decode; it returns the PCM samples produced by
// that message. Decoder state is carried across calls.
type Vocoder struct {
	n  [20]int32
	o  [20]float64
	fa int32
	g  int
	oa int32
	aa int

	// Samplerate is the rate reported by the server in 0x81 frames.
	Samplerate int

	out []int16
	j   int
}

// New returns a vocoder in its initial state.
func New() *Vocoder { return &Vocoder{g: 1, Samplerate: 8000, j: 14} }

// Reset returns the decoder to its initial state.
func (v *Vocoder) Reset() { *v = *New() }

// AGC is the current AGC gain parameter reported by the server.
func (v *Vocoder) AGC() int { return int(v.oa) }

// AudioFlags is the raw audio control byte; bit 4 selects the high-quality filter.
func (v *Vocoder) AudioFlags() int { return v.aa }

func (v *Vocoder) emit(value float64) { v.out = append(v.out, i16(value)) }

func (v *Vocoder) byteAt(data []byte, index int) int {
	if index >= 0 && index < len(data) {
		return int(data[index])
	}
	return 0
}

// decodeLPCBlock reconstructs 128 samples and returns the new byte offset.
func (v *Vocoder) decodeLPCBlock(data []byte, a, m int) int {
	for k := 0; k < 128; k++ {
		b0 := v.byteAt(data, a)
		b1 := v.byteAt(data, a+1)
		b2 := v.byteAt(data, a+2)
		b3 := v.byteAt(data, a+3)
		f := int32(int64(b3&255 | (b2&255)<<8 | (b1&255)<<16 | (b0&255)<<24))
		f = int32(int64(f) << uint(m))

		e := 0
		r := 15 - v.g
		w := float64(v.oa)
		if f != 0 {
			for uint32(f)&0x80000000 == 0 && e < r {
				f = int32(int64(f) << 1)
				e++
			}
		}
		if e < r {
			r = e
			e++
			f = int32(int64(f) << 1)
		} else {
			r = int((f >> 24) & 255)
			e += 8
			f = int32(int64(f) << 8)
		}

		s := 0
		if r >= quantizerZ[v.g] {
			s++
		}
		if r >= quantizerZ[v.g-1] {
			s++
		}
		if s > v.g-1 {
			s = v.g - 1
		}

		z := ((f >> 16) & 0xFFFF) >> uint(17-v.g)
		z &= int32(-1) << uint(s)
		z += int32(r) << uint(v.g-1)
		if f&(int32(1)<<uint(32-v.g+s)) != 0 {
			z |= (int32(1) << uint(s)) - 1
			z = ^z
		}

		m += e + v.g - s
		for m >= 8 {
			a++
			m -= 8
		}

		var accF float64
		for t := 0; t < 20; t++ {
			accF += float64(v.n[t]) * v.o[t]
		}
		acc := int64(toInt32(accF))
		if acc >= 0 {
			acc >>= 12
		} else {
			acc = (acc + 4095) >> 12
		}

		w = float64(z)*w + w/2.0
		z = toInt32(w) >> 4
		for t := 19; t >= 0; t-- {
			term := (-(v.n[t] >> 7)) + (toInt32(v.o[t]*float64(z)) >> uint(v.j))
			v.n[t] = int32(int64(v.n[t]) + int64(term))
			if t == 0 {
				break
			}
			v.o[t] = v.o[t-1]
		}
		v.o[0] = float64(acc) + w

		sample := v.o[0] + float64(v.fa>>4)
		if v.aa&16 != 0 {
			v.fa = 0
		} else {
			inner := (int64(toInt32(v.o[0])) << 4) >> 3
			v.fa = int32(int64(v.fa) + inner)
		}
		v.emit(sample)
	}
	if m == 0 {
		a--
	}
	return a
}

// fillTable handles a 0x80 frame: 128 table-looked-up samples.
//
// The reference client indexes past the end of the message when a frame is
// truncated and relies on undefined coercing to zero, so missing bytes
// contribute silence rather than shortening the frame.
func (v *Vocoder) fillTable(data []byte, a int) {
	for index := a + 1; index < a+129; index++ {
		if index < len(data) {
			v.out = append(v.out, sampleTable[data[index]])
		} else {
			v.out = append(v.out, 0)
		}
	}
	v.resetFilter()
}

// fillSilence handles a 0x84 frame: 128 zero samples.
func (v *Vocoder) fillSilence() {
	for i := 0; i < 128; i++ {
		v.out = append(v.out, 0)
	}
	v.resetFilter()
}

func (v *Vocoder) resetFilter() {
	v.n = [20]int32{}
	v.o = [20]float64{}
	v.fa = 0
}

// Decode decodes one WebSDR stream message into PCM samples.
func (v *Vocoder) Decode(data []byte) []int16 {
	v.out = nil
	a := 0
	for a < len(data) {
		c := int(data[a])
		switch {
		case c&0xF0 == 0xF0:
			a += 2
		case c == 0x80:
			v.fillTable(data, a)
			a += 129
		case c >= 0x90 && c <= 0xDF:
			v.g = 14 - (c >> 4)
			v.j = 14
			if v.aa&16 != 0 {
				v.j = 12
			}
			a = v.decodeLPCBlock(data, a, 4) + 1
		case c&0x80 == 0:
			v.j = 14
			if v.aa&16 != 0 {
				v.j = 12
			}
			a = v.decodeLPCBlock(data, a, 1) + 1
		case c == 0x81:
			v.Samplerate = 256*int(v.byteAt(data, a+1)) + int(v.byteAt(data, a+2))
			a += 3
		case c == 0x82:
			v.oa = int32(256*int(v.byteAt(data, a+1)) + int(v.byteAt(data, a+2)))
			a += 3
		case c == 0x83:
			v.aa = v.byteAt(data, a+1)
			a += 2
		case c == 0x84:
			v.fillSilence()
			a++
		case c == 0x85:
			a += 7
		case c == 0x86:
			a++
		case c == 0x87:
			a += 7
		default:
			a++
		}
	}
	return v.out
}
