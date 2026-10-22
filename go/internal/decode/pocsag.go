package decode

import (
	"fmt"
	"strings"
)

// POCSAG is the paging protocol still used by pagers and, in some places,
// by fire/ambulance alerting. It is 2-level FSK (NRZ baseband after FM
// discrimination) at 512, 1200 or 2400 baud. A transmission is a preamble
// of alternating bits, then batches of 17 codewords: a 32-bit sync word
// followed by 16 codewords of 32 bits each. Each codeword carries 20 data
// bits protected by a BCH(31,21) code plus one even parity bit.

const (
	pocsagSync = 0x7CD215D8
	pocsagIdle = 0x7A89C197
)

// POCSAGMessage is one decoded page.
type POCSAGMessage struct {
	Address  uint32
	Function int
	Numeric  string
	Alpha    string
}

// pocsagConvTable maps each 4-bit nibble to its numeric-message character.
const pocsagConvTable = "084 2.6]195-3U7["

// POCSAGDecode demodulates a POCSAG recording and returns the pages found.
// Unless baud is non-zero it tries 512, 1200 and 2400 baud in both
// polarities and keeps the combination that yields the most pages. A bit
// clock is recovered from the signal edges, so recordings of several
// bursts and mildly mistuned or drifting audio decode too.
func POCSAGDecode(samples []float64, rate int, baud int) []POCSAGMessage {
	rates := []int{1200, 512, 2400}
	if baud > 0 {
		rates = []int{baud}
	}
	var best []POCSAGMessage
	bestScore := 0
	for _, b := range rates {
		if msgs := pocsagAtBaud(samples, rate, b); pocsagScore(msgs) > bestScore {
			best, bestScore = msgs, pocsagScore(msgs)
		}
	}
	return best
}

// pocsagScore counts printable payload so that a spurious page from the
// wrong baud rate loses against a real one.
func pocsagScore(m []POCSAGMessage) int {
	n := 0
	for _, p := range m {
		n += 4 + len(p.Alpha) + len(p.Numeric)
	}
	return n
}

func pocsagAtBaud(samples []float64, rate, baud int) []POCSAGMessage {
	spb := float64(rate) / float64(baud)
	if spb < 2 || len(samples) == 0 {
		return nil
	}
	x := pocsagHighpass(samples, int(spb*48))
	var best []POCSAGMessage
	for _, invert := range []bool{false, true} {
		bits := pocsagRecoverBits(x, spb, invert)
		msgs := pocsagAllBursts(bits)
		if pocsagScore(msgs) > pocsagScore(best) {
			best = msgs
		}
	}
	return best
}

// pocsagHighpass removes slow offset by subtracting a moving average.
func pocsagHighpass(x []float64, win int) []float64 {
	if win < 3 {
		win = 3
	}
	out := make([]float64, len(x))
	var sum float64
	for i := range x {
		sum += x[i]
		if i >= win {
			sum -= x[i-win]
		}
		n := win
		if i+1 < win {
			n = i + 1
		}
		out[i] = x[i] - sum/float64(n)
	}
	return out
}

// pocsagRecoverBits slices NRZ bits with an edge-locked bit clock: the phase
// advances 1/spb per sample, is pulled towards zero at every sign change,
// and each bit is the mean of the middle half of its period.
func pocsagRecoverBits(x []float64, spb float64, invert bool) []byte {
	var bits []byte
	phase := 0.0
	step := 1 / spb
	var acc float64
	var cnt int
	prev := x[0] >= 0
	for i := range x {
		cur := x[i] >= 0
		if cur != prev {
			e := phase
			if e > 0.5 {
				e -= 1
			}
			phase -= 0.08 * e
		}
		prev = cur
		phase += step
		if phase >= 0.25 && phase < 0.75 {
			acc += x[i]
			cnt++
		}
		if phase >= 1 {
			phase -= 1
			v := acc >= 0
			if cnt == 0 {
				v = cur
			}
			if invert {
				v = !v
			}
			if v {
				bits = append(bits, 1)
			} else {
				bits = append(bits, 0)
			}
			acc, cnt = 0, 0
		}
	}
	return bits
}

func popcount32(x uint32) int {
	n := 0
	for ; x != 0; x &= x - 1 {
		n++
	}
	return n
}

// pocsagAllBursts finds every sync word (allowing two bit errors) and
// decodes the batches that follow it, resuming the search wherever a burst
// ends.
func pocsagAllBursts(bits []byte) []POCSAGMessage {
	var out []POCSAGMessage
	i := 0
	var w uint32
	for k := 0; k < 32 && k < len(bits); k++ {
		w = w<<1 | uint32(bits[k])
	}
	for pos := 0; pos+32 <= len(bits); {
		if pos > 0 {
			w = w<<1 | uint32(bits[pos+31])
		}
		if popcount32(w^pocsagSync) <= 2 {
			end := pocsagBurstEnd(bits, pos)
			out = append(out, pocsagFromBits(bits[pos:end])...)
			pos = end
			if pos+32 <= len(bits) {
				w = 0
				for k := 0; k < 32; k++ {
					w = w<<1 | uint32(bits[pos+k])
				}
			}
			continue
		}
		pos++
	}
	_ = i
	return out
}

// pocsagBurstEnd returns where the run of batches starting at start stops:
// the first place a batch does not begin with a (nearly) correct sync word.
func pocsagBurstEnd(bits []byte, start int) int {
	pos := start
	for pos+32*17 <= len(bits) {
		if popcount32(bitsToUint32(bits[pos:pos+32])^pocsagSync) > 4 {
			break
		}
		pos += 32 * 17
	}
	if pos == start {
		pos = start + 32*17
		if pos > len(bits) {
			pos = len(bits)
		}
	}
	return pos
}

// pocsagSliceBits integrates the signal over each bit period (a matched
// filter for NRZ) starting at the given sub-sample offset, and returns one
// bit per period.
func pocsagSliceBits(centered []float64, spb, phase float64) []byte {
	n := len(centered)
	var bits []byte
	for pos := phase; pos+spb <= float64(n); pos += spb {
		start := int(pos)
		end := int(pos + spb)
		if end > n {
			end = n
		}
		var acc float64
		for i := start; i < end; i++ {
			acc += centered[i]
		}
		if acc >= 0 {
			bits = append(bits, 1)
		} else {
			bits = append(bits, 0)
		}
	}
	return bits
}

// indexSync finds the sync codeword in a bit stream, MSB first.
func indexSync(bits []byte) int {
	for i := 0; i+32 <= len(bits); i++ {
		if bitsToUint32(bits[i:i+32]) == pocsagSync {
			return i
		}
	}
	return -1
}

func bitsToUint32(b []byte) uint32 {
	var v uint32
	for _, bit := range b {
		v = v<<1 | uint32(bit)
	}
	return v
}

// pocsagFromBits walks batches after the sync word, decodes address and
// message codewords, and assembles pages. It follows multimon-ng's model:
// a batch is 16 data codewords, the address codeword sits in the frame
// given by the address's low three bits, and the 18-bit address in the
// codeword supplies the upper bits.
func pocsagFromBits(bits []byte) []POCSAGMessage {
	var out []POCSAGMessage
	var cur *POCSAGMessage
	var data []byte // message data bits, MSB first

	flush := func() {
		if cur == nil {
			return
		}
		if cur.Function == 0 {
			cur.Numeric = pocsagNumeric(data)
		} else {
			cur.Alpha = pocsagAlpha(data)
		}
		out = append(out, *cur)
		cur = nil
		data = data[:0]
	}

	slot := 0 // data-codeword index within the batch (0..15)
	for i := 0; i+32 <= len(bits); i += 32 {
		word := bitsToUint32(bits[i : i+32])
		if popcount32(word^pocsagSync) <= 4 {
			// A sync word separates batches; the current message continues
			// across it, so do not flush here.
			slot = 0
			continue
		}
		frame := slot >> 1
		slot = (slot + 1) % 16
		if word == pocsagIdle {
			flush()
			continue
		}
		var valid bool
		word, valid = pocsagCorrectOK(word)
		// All-zero and all-one words are valid codewords but only appear
		// when the slicer is looking at silence or a carrier.
		if !valid || word == 0 || word == 0xFFFFFFFF {
			// Uncorrectable: an erasure. End the current page rather than
			// splice garbage into it.
			flush()
			continue
		}
		if word>>31&1 == 0 {
			// Address codeword: start a new page.
			flush()
			// Address codeword: the top 18 address bits are in the data
			// field; the low three come from the frame number.
			addr := (((word >> 13) & 0x3FFFF) << 3) | uint32(frame)
			cur = &POCSAGMessage{Address: addr, Function: int((word >> 11) & 3)}
		} else if cur != nil {
			// Message codeword: 20 data bits, bits 30..11.
			for b := 30; b >= 11; b-- {
				data = append(data, byte((word>>uint(b))&1))
			}
		}
	}
	flush()
	return out
}

// pocsagNumeric decodes the 20-bit-per-codeword numeric format: each nibble
// is one digit from the conversion table.
func pocsagNumeric(bits []byte) string {
	var out strings.Builder
	nibbles := len(bits) / 4
	for i := 0; i < nibbles; i++ {
		v := 0
		for b := 0; b < 4; b++ {
			v = v<<1 | int(bits[i*4+b])
		}
		out.WriteByte(pocsagConvTable[v&0xF])
	}
	return strings.TrimRight(out.String(), " ")
}

// pocsagAlpha decodes the 7-bit-per-character alphanumeric format. Each
// character is sent MSB-first but bit-reversed within its 7 bits.
func pocsagAlpha(bits []byte) string {
	var out strings.Builder
	for i := 0; i+7 <= len(bits); i += 7 {
		var v byte
		for b := 0; b < 7; b++ {
			v = v<<1 | bits[i+b]
		}
		ch := rev7(v)
		if ch >= 32 && ch < 127 {
			out.WriteByte(ch)
		}
	}
	return strings.TrimSpace(out.String())
}

func rev7(b byte) byte {
	return ((b << 6) & 64) | ((b >> 6) & 1) |
		((b << 4) & 32) | ((b >> 4) & 2) |
		((b << 2) & 16) | ((b >> 2) & 4) |
		(b & 8)
}

// POCSAG BCH(31,21): generator polynomial x^10+x^9+x^8+x^6+x^5+x^3+1
// (0x769). The parity and syndrome tables below are built the same way
// multimon-ng builds them, by reducing each single bit position modulo the
// generator, so this decoder agrees with the reference on real pager
// traffic.
const pocsagPoly = 0x769

func pocsagReduce(shreg uint32) uint32 {
	for i := 20; i >= 0; i-- {
		if shreg&(1<<uint(i+10)) != 0 {
			shreg ^= pocsagPoly << uint(i)
		}
	}
	return shreg & 0x3FF
}

// pocsagParityTbl[i] is the 10-bit BCH parity contributed by data bit i
// (i = 0..20, counting from the codeword's bit 11).
var pocsagParityTbl = func() [21]uint32 {
	var t [21]uint32
	for i := 0; i < 21; i++ {
		t[i] = pocsagReduce(uint32(1) << uint(i+10))
	}
	return t
}()

// pocsagSynTbl[i] is the syndrome contributed by codeword bit i+1.
var pocsagSynTbl = func() [31]uint32 {
	var t [31]uint32
	for i := 0; i < 31; i++ {
		t[i] = pocsagReduce(uint32(1) << uint(i))
	}
	return t
}()

// pocsagErrTbl maps a syndrome to the 1- or 2-bit error pattern that
// produces it. A single-bit error always flips the overall parity bit, so
// its syndrome carries the 0x400 flag; for a two-bit error the two parity
// flips cancel and the flag is absent.
var pocsagErrTbl = func() map[uint32]uint32 {
	m := make(map[uint32]uint32, 512)
	for i := 1; i < 32; i++ {
		m[pocsagSynTbl[i-1]|0x400] = 1 << uint(i)
	}
	for i := 1; i < 32; i++ {
		for j := i + 1; j < 32; j++ {
			key := pocsagSynTbl[i-1] ^ pocsagSynTbl[j-1]
			if _, ok := m[key]; !ok {
				m[key] = (1 << uint(i)) | (1 << uint(j))
			}
		}
	}
	return m
}()

// pocsagSyndrome computes the BCH syndrome of a codeword over bits 1-31,
// plus the 0x400 flag when the overall even-parity bit is wrong.
func pocsagSyndrome(word uint32) uint32 {
	bits := word >> 1
	var syn uint32
	for bit := 0; bit < 31; bit++ {
		if bits&(1<<uint(bit)) != 0 {
			syn ^= pocsagSynTbl[bit]
		}
	}
	if parity32(word) != 0 {
		syn |= 0x400
	}
	return syn
}

// pocsagCorrect applies BCH(31,21) single/double error correction and
// returns the corrected codeword. The error table already accounts for the
// overall parity bit, matching multimon-ng's bch_pocsag_correct.
func pocsagCorrect(word uint32) uint32 {
	w, _ := pocsagCorrectOK(word)
	return w
}

// pocsagCorrectOK is pocsagCorrect that also reports whether the codeword
// is valid after correction (false: too many errors to trust).
func pocsagCorrectOK(word uint32) (uint32, bool) {
	if syn := pocsagSyndrome(word); syn != 0 {
		err, ok := pocsagErrTbl[syn]
		if !ok {
			return word, false
		}
		word ^= err
	}
	return word, true
}

func parity32(x uint32) uint32 {
	x ^= x >> 16
	x ^= x >> 8
	x ^= x >> 4
	x ^= x >> 2
	x ^= x >> 1
	return x & 1
}

// POCSAGMessageString formats a page for display.
func (m POCSAGMessage) String() string {
	text := m.Alpha
	if m.Function == 0 {
		text = m.Numeric
	}
	if text == "" {
		text = m.Alpha
	}
	return fmt.Sprintf("addr %d func %d: %s", m.Address, m.Function, text)
}
