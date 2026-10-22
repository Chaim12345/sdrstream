package decode

import (
	"fmt"
	"strings"
)

// FLEX (Motorola) paging, 1600 baud 2-level FSK, phase A only.
//
// The frame layout, BCH conventions and alphanumeric message rules follow
// multimon-ng's demod_flex.c (GPL-2.0-or-later, (C) Elias Oenal and
// contributors, with Tom Sailer's original multimon), which this decoder was
// written against and is cross-checked with. Not implemented: 3200 baud,
// 4-level FSK, phases B-D, group calls, long addresses and numeric pages.

const (
	flexMarker    = 0xA6C6AAAA
	flexSync1600  = 0x870C // sync code for 1600 baud, 2-level
	flexPhaseWord = 88
)

// FlexMessage is one decoded FLEX page.
type FlexMessage struct {
	Capcode  int64
	Cycle    int
	Frame    int
	Type     int  // vector type, 5 = alphanumeric
	Fragment byte // 'K' complete, 'F' first/continued, 'C' final fragment
	Text     string
}

func (m FlexMessage) String() string {
	return fmt.Sprintf("FLEX|1600/2/%c/A|%02d.%03d|%09d|ALN|%s", m.Fragment, m.Cycle, m.Frame, m.Capcode, m.Text)
}

const flexGen = 0x769 // x^10+x^9+x^8+x^6+x^5+x^3+1, same BCH(31,21) as POCSAG

func flexRev31(w uint32) uint32 {
	var r uint32
	for i := 0; i < 31; i++ {
		r |= (w >> uint(i) & 1) << uint(30-i)
	}
	return r
}

func flexRem(std uint32) uint32 {
	for i := 30; i >= 10; i-- {
		if std>>uint(i)&1 == 1 {
			std ^= flexGen << uint(i-10)
		}
	}
	return std
}

var flexFix map[uint32]uint32 // syndrome -> error pattern (std bit order), up to 2 errors

func init() {
	flexFix = map[uint32]uint32{}
	for i := 0; i < 31; i++ {
		flexFix[flexRem(1<<uint(i))] = 1 << uint(i)
	}
	for i := 0; i < 31; i++ {
		for j := i + 1; j < 31; j++ {
			e := uint32(1)<<uint(i) | uint32(1)<<uint(j)
			s := flexRem(e)
			if _, ok := flexFix[s]; !ok {
				flexFix[s] = e
			}
		}
	}
}

// flexCorrect checks a received 32-bit FLEX codeword (bit k = k-th bit on
// the air) and corrects up to two errors. It returns the 21 data bits.
func flexCorrect(w uint32) (uint32, bool) {
	std := flexRev31(w & 0x7FFFFFFF)
	if s := flexRem(std); s != 0 {
		e, ok := flexFix[s]
		if !ok {
			return 0, false
		}
		std ^= e
	}
	return flexRev31(std) & 0x1FFFFF, true
}

// FlexDecode demodulates a recording and returns FLEX pages.
func FlexDecode(samples []float64, rate int) []FlexMessage {
	const baud = 1600
	spb := float64(rate) / baud
	if spb < 2 || len(samples) == 0 {
		return nil
	}
	mean := 0.0
	for _, s := range samples {
		mean += s
	}
	mean /= float64(len(samples))
	c := make([]float64, len(samples))
	for i, s := range samples {
		c[i] = s - mean
	}
	var out []FlexMessage
	seen := map[string]bool{}
	for phase := 0.0; phase < spb; phase++ {
		bits := pocsagSliceBits(c, spb, phase)
		for _, m := range flexFromBits(bits) {
			k := fmt.Sprintf("%d|%d|%d|%s", m.Capcode, m.Cycle, m.Frame, m.Text)
			if !seen[k] {
				seen[k] = true
				out = append(out, m)
			}
		}
	}
	return out
}

func flexBitsSync(bits []byte, at int, invert bool) bool {
	// sync buffer bit = !stream bit; AAAA:BBBBBBBB:CCCC, tolerate <4 errors in
	// the marker and in the outer-code complement check.
	var buf uint64
	for i := 0; i < 64; i++ {
		b := bits[at+i]
		if invert {
			b ^= 1
		}
		buf = buf<<1 | uint64(b^1)
	}
	marker := uint32(buf >> 16)
	hi := uint16(buf >> 48)
	lo := ^uint16(buf)
	pop := func(x uint32) int {
		n := 0
		for ; x != 0; x &= x - 1 {
			n++
		}
		return n
	}
	if pop(marker^flexMarker) >= 4 || pop(uint32(lo^hi)) >= 4 {
		return false
	}
	return pop(uint32(hi^flexSync1600)) < 4
}

func flexFromBits(bits []byte) []FlexMessage {
	var out []FlexMessage
	const frameBits = 64 + 16 + 32 + 40 + 2816
	for i := 0; i+frameBits <= len(bits); i++ {
		for _, inv := range []bool{false, true} {
			if !flexBitsSync(bits, i, inv) {
				continue
			}
			b := bits[i+64 : i+frameBits]
			if inv {
				nb := make([]byte, len(b))
				for k := range b {
					nb[k] = b[k] ^ 1
				}
				b = nb
			}
			var fiwRaw uint32
			for k := 0; k < 32; k++ {
				fiwRaw |= uint32(b[16+k]) << uint(k)
			}
			fiw, ok := flexCorrect(fiwRaw)
			if !ok {
				continue
			}
			sum := fiw&0xF + fiw>>4&0xF + fiw>>8&0xF + fiw>>12&0xF + fiw>>16&0xF + fiw>>20&1
			if sum&0xF != 0xF {
				continue
			}
			data := b[16+32+40:]
			var words [flexPhaseWord]uint32
			for blk := 0; blk < 11; blk++ {
				for j := 0; j < 256; j++ {
					words[blk*8+j&7] |= uint32(data[blk*256+j]) << uint(j>>3)
				}
			}
			out = append(out, flexParsePhase(words[:], int(fiw>>4&0xF), int(fiw>>8&0x7F))...)
			i += frameBits - 1
			break
		}
	}
	return out
}

func flexParsePhase(raw []uint32, cycle, frame int) []FlexMessage {
	w := make([]uint32, len(raw))
	for i, r := range raw {
		d, ok := flexCorrect(r)
		if !ok {
			return nil // garbled block: multimon-ng also drops the phase
		}
		w[i] = d
	}
	biw := w[0]
	if biw == 0 || biw == 0x1FFFFF {
		return nil
	}
	voff := int(biw >> 10 & 0x3F)
	aoff := int(biw>>8&3) + 1
	if voff < aoff || voff+(voff-aoff) > len(w) {
		return nil
	}
	var out []FlexMessage
	for i := aoff; i < voff; i++ {
		if w[i] == 0 || w[i] == 0x1FFFFF {
			continue
		}
		capcode := int64(w[i]) - 0x8000
		if capcode >= 2029568 && capcode <= 2029583 { // group call addresses
			continue
		}
		viw := w[voff+i-aoff]
		typ := int(viw >> 4 & 7)
		mw1 := int(viw >> 7 & 0x7F)
		n := int(viw >> 14 & 0x7F)
		if typ != 5 || n < 1 || mw1+n > len(w) {
			continue
		}
		hdr := w[mw1]
		frag := hdr >> 11 & 3
		cont := hdr >> 10 & 1
		flag := byte('K')
		switch {
		case cont == 1:
			flag = 'F'
		case frag != 3:
			flag = 'C'
		}
		var sb strings.Builder
		for k := mw1 + 1; k <= mw1+n-1; k++ {
			d := w[k]
			if k > mw1+1 || frag != 3 {
				if ch := byte(d & 0x7F); ch != 3 {
					sb.WriteByte(ch)
				}
			}
			for _, sh := range []uint{7, 14} {
				if ch := byte(d >> sh & 0x7F); ch != 3 {
					sb.WriteByte(ch)
				}
			}
		}
		out = append(out, FlexMessage{Capcode: capcode, Cycle: cycle, Frame: frame, Type: typ, Fragment: flag, Text: strings.TrimRight(sb.String(), "\x00 ")})
	}
	return out
}
