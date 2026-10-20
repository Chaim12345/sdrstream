package decode

import (
	"math"
	"strings"
)

// ita2Letters and ita2Figures are the ITA2 (Baudot) code tables. The five
// data bits are indexed LSB first, matching how a teleprinter sends them
// (start bit, then bit 1 ... bit 5, then stop). The standard 45.45 baud RTTY
// shift is 170 Hz with the mark (1) as the higher tone.
var ita2Letters = [32]byte{
	0, 'E', '\n', 'A', ' ', 'S', 'I', 'U',
	'\r', 'D', 'R', 'J', 'N', 'F', 'C', 'K',
	'T', 'Z', 'L', 'W', 'H', 'Y', 'P', 'Q',
	'O', 'B', 'G', 0, 'M', 'X', 'V', 0,
}

var ita2Figures = [32]byte{
	0, '3', '\n', '-', ' ', '\'', '8', '7',
	'\r', '$', '4', 0, ',', '!', ':', '(',
	'5', '"', ')', '2', '#', '6', '0', '1',
	'9', '?', '&', 0, '.', '/', ';', 0,
}

const (
	ita2ShiftFigures = 0x1B // 11011
	ita2ShiftLetters = 0x1F // 11111
)

// RTTYDecode demodulates a standard 45.45 baud, 170 Hz shift RTTY signal.
// The bit convention is one start bit (space), five data bits LSB first,
// then one or more stop bits (mark). Characters are accumulated with ITA2
// letters/figures shifting.
func RTTYDecode(samples []float64, rate int, markHz, spaceHz, baud float64) string {
	if rate < 4000 || len(samples) < rate/10 {
		return ""
	}
	if markHz == 0 {
		markHz = 2125
	}
	if spaceHz == 0 {
		spaceHz = markHz - 170
	}
	if baud <= 0 {
		baud = 45.45
	}
	spb := float64(rate) / baud
	win := int(math.Round(spb * 0.8))
	if win < 8 {
		win = 8
	}
	// Build an oversampled mark/space decision stream. For each 1/8-bit step
	// compare the windowed energy of the mark and space tones; the sign is
	// the instantaneous bit value. Oversampling lets the framer lock onto
	// each start edge instead of relying on a free-running bit clock.
	const over = 8
	step := spb / over
	var level []byte
	for pos := 0.0; pos+float64(win) <= float64(len(samples)); pos += step {
		start := int(pos)
		seg := samples[start : start+win]
		markMag := goertzel(seg, rate, markHz)
		spaceMag := goertzel(seg, rate, spaceHz)
		b := byte(0)
		if markMag >= spaceMag {
			b = 1
		}
		level = append(level, b)
	}
	return ita2Frame(level, over)
}

// ita2Frame locks onto start bits in an oversampled mark/space stream
// (samplesPerBit = over entries per bit) and decodes ITA2 characters.
func ita2Frame(level []byte, over int) string {
	var out strings.Builder
	figures := false
	i := 1
	for i+7*over < len(level) {
		// A start bit is a mark->space edge.
		if !(level[i] == 0 && level[i-1] == 1) {
			i++
			continue
		}
		// Sample each bit at its centre: the start bit spans i..i+over.
		sample := func(bit int) byte { return level[i+bit*over+over/2] }
		code := 0
		for b := 0; b < 5; b++ {
			if sample(b+1) == 1 {
				code |= 1 << b
			}
		}
		if sample(6) != 1 { // stop bit must be mark
			i++
			continue
		}
		switch code {
		case ita2ShiftLetters:
			figures = false
		case ita2ShiftFigures:
			figures = true
		default:
			var ch byte
			if figures {
				ch = ita2Figures[code]
			} else {
				ch = ita2Letters[code]
			}
			if ch != 0 {
				out.WriteByte(ch)
			}
		}
		i += 7 * over
	}
	return out.String()
}
