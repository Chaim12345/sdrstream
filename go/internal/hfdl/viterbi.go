package hfdl

// viterbi.go is a K=7 rate-1/2 soft-decision Viterbi decoder, ported from
// the portable C decoder in libfec by Phil Karn, KA9Q (Feb 2004), as
// shipped in dumphfdl (src/libfec/viterbi27_port.c). Original licence:
// GNU Lesser General Public License. Polynomials 0x6d and 0x4f.

const (
	v27PolyA = 0x6d
	v27PolyB = 0x4f
)

var branchtab27 [2][32]uint8

func init() {
	parity := func(x int) int {
		p := 0
		for ; x != 0; x >>= 1 {
			p ^= x & 1
		}
		return p
	}
	for state := 0; state < 32; state++ {
		if parity((2*state)&v27PolyA) != 0 {
			branchtab27[0][state] = 255
		}
		if parity((2*state)&v27PolyB) != 0 {
			branchtab27[1][state] = 255
		}
	}
}

// viterbiDecode decodes nbits data bits from 2*nbits soft symbols (0..255,
// 0 = strong zero, 255 = strong one) starting and ending in state 0, and
// returns the bits packed MSB-first-in-time into bytes the way libfec does
// (the caller bit-reverses each byte, see Decode).
func viterbiDecode(syms []uint8, nbits int) []byte {
	var m1, m2 [64]uint32
	old, nw := &m1, &m2
	for i := range old {
		old[i] = 63
	}
	old[0] = 0
	decisions := make([][2]uint32, nbits+6)
	for n := 0; n < nbits; n++ {
		d := &decisions[n]
		sym0, sym1 := syms[2*n], syms[2*n+1]
		for i := 0; i < 32; i++ {
			metric := uint32(branchtab27[0][i]^sym0) + uint32(branchtab27[1][i]^sym1)
			mm0 := old[i] + metric
			mm1 := old[i+32] + (510 - metric)
			var dec uint32
			if int32(mm0-mm1) > 0 {
				dec = 1
			}
			if dec == 1 {
				nw[2*i] = mm1
			} else {
				nw[2*i] = mm0
			}
			d[i/16] |= dec << uint((2*i)&31)
			mm0 -= metric + metric - 510
			mm1 += metric + metric - 510
			dec = 0
			if int32(mm0-mm1) > 0 {
				dec = 1
			}
			if dec == 1 {
				nw[2*i+1] = mm1
			} else {
				nw[2*i+1] = mm0
			}
			d[i/16] |= dec << uint((2*i+1)&31)
		}
		old, nw = nw, old
	}
	out := make([]byte, (nbits+7)/8)
	endstate := uint32(0) << 2
	// libfec chains back from d+6: bit n comes from the decision made 6
	// steps later (the register holds 6 bits), and steps past the block
	// stay zero, so the last 6 bits of a block are only right if they are
	// zero (HFDL pads its PDUs).
	for n := nbits - 1; n >= 0; n-- {
		st := endstate >> 2
		k := (decisions[n+6][st/32] >> (st % 32)) & 1
		endstate = (endstate >> 1) | (k << 7)
		out[n>>3] = byte(endstate)
	}
	return out
}

func reverseByte(b byte) byte {
	var r byte
	for i := 0; i < 8; i++ {
		r = r<<1 | b&1
		b >>= 1
	}
	return r
}

// viterbiEncode is a reference encoder matching the decoder's convention,
// used by tests. bits are fed MSB-first within each byte after the same
// bit-reversal Decode applies.
func viterbiEncode(data []byte) []uint8 {
	parity := func(x uint) uint8 {
		var p uint8
		for ; x != 0; x >>= 1 {
			p ^= uint8(x & 1)
		}
		return p
	}
	var reg uint
	var out []uint8
	emit := func(b uint) {
		reg = ((reg << 1) | b) & 0x7f
		for _, poly := range []uint{v27PolyA, v27PolyB} {
			if parity(reg&poly) == 1 {
				out = append(out, 255)
			} else {
				out = append(out, 0)
			}
		}
	}
	for _, by := range data {
		by = reverseByte(by)
		for i := 7; i >= 0; i-- {
			emit(uint(by>>uint(i)) & 1)
		}
	}
	for i := 0; i < 6; i++ {
		emit(0)
	}
	return out
}
