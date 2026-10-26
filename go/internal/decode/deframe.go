package decode

// deframe.go: bit-level deframers that sit between a demodulator's hard
// bits and the telemetry parsers, for the framings gr-satellites (and so
// sgoudelis/ground-station, GPL-3.0, pinned 351631d3066b) uses and that
// sdrstream lacked: the G3RUH scrambler of 9k6 AX.25, and the CCSDS
// synchronisation marker + pseudo-randomiser + Reed-Solomon(255,223)
// frame. Written from the public CCSDS 131.0-B and G3RUH specifications,
// not copied from upstream.

import "errors"

// G3RUHDescramble undoes the G3RUH self-synchronising scrambler
// (x^17 + x^12 + 1) on a hard-bit stream: out[n] = in[n]^in[n-12]^in[n-17].
// Because it is self-synchronising there is no state to align.
func G3RUHDescramble(bits []bool) []bool {
	out := make([]bool, len(bits))
	for n := range bits {
		v := bits[n]
		if n >= 12 && bits[n-12] {
			v = !v
		}
		if n >= 17 && bits[n-17] {
			v = !v
		}
		out[n] = v
	}
	return out
}

// G3RUHScramble is the transmit side, used for tests and synthetic vectors.
func G3RUHScramble(bits []bool) []bool {
	out := make([]bool, len(bits))
	for n := range bits {
		v := bits[n]
		if n >= 12 && out[n-12] {
			v = !v
		}
		if n >= 17 && out[n-17] {
			v = !v
		}
		out[n] = v
	}
	return out
}

// CCSDSASM is the 32-bit attached sync marker.
const CCSDSASM uint32 = 0x1ACFFC1D

// ccsdsPN returns n bytes of the CCSDS pseudo-randomiser
// (x^8+x^7+x^5+x^3+1, all-ones seed); it begins FF 48 0E C0 9A 0D 70 BC.
func ccsdsPN(n int) []byte {
	out := make([]byte, n)
	reg := uint8(0xFF)
	for i := 0; i < n*8; i++ {
		bit := reg & 1 // output is the register's last stage
		out[i/8] |= bit << (7 - uint(i%8))
		fb := (reg ^ reg>>3 ^ reg>>5 ^ reg>>7) & 1
		reg = reg>>1 | fb<<7
	}
	return out
}

// CCSDSDerandomize XORs data with the pseudo-random sequence in place.
func CCSDSDerandomize(data []byte) {
	pn := ccsdsPN(len(data))
	for i := range data {
		data[i] ^= pn[i]
	}
}

// FindASM returns every bit offset where the ASM matches within maxErr
// bit errors, trying both polarities; inverted reports a 180-degree flip.
type ASMHit struct {
	Offset   int // index of the first ASM bit
	Inverted bool
	Errors   int
}

func FindASM(bits []bool, maxErr int) []ASMHit {
	var hits []ASMHit
	for off := 0; off+32 <= len(bits); off++ {
		var w uint32
		for i := 0; i < 32; i++ {
			w <<= 1
			if bits[off+i] {
				w |= 1
			}
		}
		e := popcount32(w ^ CCSDSASM)
		if e <= maxErr {
			hits = append(hits, ASMHit{off, false, e})
		} else if e2 := 32 - e; e2 <= maxErr {
			hits = append(hits, ASMHit{off, true, e2})
		}
	}
	return hits
}

// ---- Reed-Solomon (255,223), CCSDS parameters -------------------------

const (
	rsN      = 255
	rsK      = 223
	rsNroots = 32
	rsFcr    = 112
	rsPrim   = 11
)

var (
	gfExp [512]byte
	gfLog [256]int
	rsGen []byte // generator polynomial, highest degree first
	// talToConv maps a dual-basis (Berlekamp) byte to conventional basis.
	// Source: widely published CCSDS Tal matrix. NOT verified here against
	// a real dual-basis frame; see RSDecode dualBasis note.
	talToConv = [8]byte{0x8d, 0xef, 0xec, 0x86, 0xfa, 0x99, 0xaf, 0x7b}
	convToTal [256]byte
	talTab    [256]byte
)

func init() {
	x := 1
	for i := 0; i < 255; i++ {
		gfExp[i] = byte(x)
		gfLog[x] = i
		x <<= 1
		if x&0x100 != 0 {
			x ^= 0x187
		}
	}
	for i := 255; i < 512; i++ {
		gfExp[i] = gfExp[i-255]
	}
	// Generator: prod (x - a^((fcr+i)*prim)).
	rsGen = []byte{1}
	for i := 0; i < rsNroots; i++ {
		root := gfExp[((rsFcr+i)*rsPrim)%255]
		next := make([]byte, len(rsGen)+1)
		for j, c := range rsGen {
			next[j] ^= c
			next[j+1] ^= gfMul(c, root)
		}
		rsGen = next
	}
	// Tal tables: talTab[tal] = conventional; convToTal is its inverse.
	for v := 0; v < 256; v++ {
		var r byte
		for b := 0; b < 8; b++ {
			if v&(0x80>>uint(b)) != 0 {
				r ^= talToConv[b]
			}
		}
		talTab[v] = r
		convToTal[r] = byte(v)
	}
}

func gfMul(a, b byte) byte {
	if a == 0 || b == 0 {
		return 0
	}
	return gfExp[gfLog[a]+gfLog[b]]
}

func gfInv(a byte) byte { return gfExp[255-gfLog[a]] }

// RSEncode returns the 255-byte codeword (data then 32 parity) for up to
// 223 data bytes (shorter input is zero-padded virtually, not transmitted
// in the output: the result has len(data)+32 bytes).
func RSEncode(data []byte) []byte {
	parity := make([]byte, rsNroots)
	for _, d := range data {
		fb := d ^ parity[0]
		copy(parity, parity[1:])
		parity[rsNroots-1] = 0
		if fb != 0 {
			for j := 0; j < rsNroots; j++ {
				parity[j] ^= gfMul(fb, rsGen[j+1])
			}
		}
	}
	return append(append([]byte{}, data...), parity...)
}

var ErrRSUncorrectable = errors.New("reed-solomon: uncorrectable")

// RSDecode corrects up to 16 byte errors in cw (len 33..255, shortened
// codes allowed) in place and returns the number corrected.
func RSDecode(cw []byte) (int, error) {
	n := len(cw)
	if n <= rsNroots || n > rsN {
		return 0, errors.New("reed-solomon: bad length")
	}
	pad := rsN - n // virtual leading zeros
	// Syndromes S_i = c(a^((fcr+i)*prim)).
	syn := make([]byte, rsNroots)
	nz := false
	for i := 0; i < rsNroots; i++ {
		root := gfExp[((rsFcr+i)*rsPrim)%255]
		var s byte
		for _, c := range cw {
			s = gfMul(s, root) ^ c
		}
		syn[i] = s
		if s != 0 {
			nz = true
		}
	}
	if !nz {
		return 0, nil
	}
	// Berlekamp-Massey. lambda low-degree-first.
	lambda := make([]byte, rsNroots+1)
	b := make([]byte, rsNroots+1)
	lambda[0], b[0] = 1, 1
	L, m := 0, 1
	var bb byte = 1
	for r := 0; r < rsNroots; r++ {
		var d byte = syn[r]
		for i := 1; i <= L; i++ {
			d ^= gfMul(lambda[i], syn[r-i])
		}
		if d == 0 {
			m++
			continue
		}
		t := append([]byte{}, lambda...)
		coef := gfMul(d, gfInv(bb))
		for i := 0; i+m <= rsNroots; i++ {
			lambda[i+m] ^= gfMul(coef, b[i])
		}
		if 2*L <= r {
			L = r + 1 - L
			b = t
			bb = d
			m = 1
		} else {
			m++
		}
	}
	if L > rsNroots/2 {
		return 0, ErrRSUncorrectable
	}
	// Chien search over actual positions. Position p (0 = first byte of cw)
	// has locator X = a^(prim*(n-1-p)) in the full-length code index.
	var locs []int
	var xs []byte
	for p := 0; p < n; p++ {
		e := (rsN - 1 - (p + pad)) // power index of the byte in the codeword
		xinv := gfExp[(255-(rsPrim*e)%255)%255]
		var v byte
		xp := byte(1)
		for i := 0; i <= L; i++ {
			v ^= gfMul(lambda[i], xp)
			xp = gfMul(xp, xinv)
		}
		if v == 0 {
			locs = append(locs, p)
			xs = append(xs, gfExp[(rsPrim*e)%255])
		}
	}
	if len(locs) != L {
		return 0, ErrRSUncorrectable
	}
	// Omega = S(x)*lambda(x) mod x^nroots.
	omega := make([]byte, rsNroots)
	for i := 0; i < rsNroots; i++ {
		for j := 0; j <= i && j <= L; j++ {
			omega[i] ^= gfMul(lambda[j], syn[i-j])
		}
	}
	// Forney with general fcr: e = X^(1-fcr) * omega(X^-1) / lambda'(X^-1)
	// (using the prim-stepped locator).
	for k, p := range locs {
		x := xs[k]
		xinv := gfInv(x)
		var num byte
		xp := byte(1)
		for i := 0; i < rsNroots; i++ {
			num ^= gfMul(omega[i], xp)
			xp = gfMul(xp, xinv)
		}
		var den byte
		// formal derivative: odd terms only.
		xp = 1
		for i := 1; i <= L; i += 2 {
			// term lambda[i] * xinv^(i-1)
			xp = byte(1)
			for j := 0; j < i-1; j++ {
				xp = gfMul(xp, xinv)
			}
			den ^= gfMul(lambda[i], xp)
		}
		if den == 0 {
			return 0, ErrRSUncorrectable
		}
		// X^(1-fcr) factor.
		pw := ((1-rsFcr)%255 + 255) % 255
		xf := byte(1)
		if x != 0 {
			xf = gfExp[(gfLog[x]*pw)%255]
		}
		mag := gfMul(xf, gfMul(num, gfInv(den)))
		cw[p] ^= mag
	}
	// Verify.
	for i := 0; i < rsNroots; i++ {
		root := gfExp[((rsFcr+i)*rsPrim)%255]
		var s byte
		for _, c := range cw {
			s = gfMul(s, root) ^ c
		}
		if s != 0 {
			return 0, ErrRSUncorrectable
		}
	}
	return L, nil
}

// CCSDSFrame is one decoded transfer frame.
type CCSDSFrame struct {
	Offset    int    // bit offset of the ASM in the input
	Inverted  bool   // stream polarity was flipped
	Corrected int    // RS byte errors fixed (summed over interleaved codewords)
	Data      []byte // frame data, parity stripped
}

// CCSDSDeframe finds ASMs in a hard-bit stream, derandomises the following
// frame of dataLen bytes (<=223*interleave), RS-decodes each of the
// `interleave` codewords (dual basis if dual), and returns frames whose
// every codeword decoded. Frames that fail RS are dropped, so a returned
// frame has passed an error-correcting check.
func CCSDSDeframe(bits []bool, dataLen, interleave int, dual bool, asmMaxErr int) []CCSDSFrame {
	if interleave < 1 {
		interleave = 1
	}
	per := dataLen / interleave
	cwLen := per + rsNroots
	total := cwLen * interleave
	var out []CCSDSFrame
	for _, h := range FindASM(bits, asmMaxErr) {
		start := h.Offset + 32
		if start+total*8 > len(bits) {
			continue
		}
		raw := make([]byte, total)
		for i := 0; i < total*8; i++ {
			v := bits[start+i]
			if h.Inverted {
				v = !v
			}
			if v {
				raw[i/8] |= 0x80 >> uint(i%8)
			}
		}
		CCSDSDerandomize(raw)
		fr := CCSDSFrame{Offset: h.Offset, Inverted: h.Inverted}
		ok := true
		data := make([]byte, 0, dataLen)
		cws := make([][]byte, interleave)
		for i := range cws {
			cws[i] = make([]byte, cwLen)
		}
		for i, b := range raw {
			cws[i%interleave][i/interleave] = b
		}
		for _, cw := range cws {
			if dual {
				for i := range cw {
					cw[i] = talTab[cw[i]]
				}
			}
			n, err := RSDecode(cw)
			if err != nil {
				ok = false
				break
			}
			fr.Corrected += n
		}
		if !ok {
			continue
		}
		for j := 0; j < per; j++ {
			for i := 0; i < interleave; i++ {
				data = append(data, cws[i][j])
			}
		}
		if dual {
			for i := range data {
				data[i] = convToTal[data[i]]
			}
		}
		fr.Data = data
		out = append(out, fr)
	}
	return out
}
