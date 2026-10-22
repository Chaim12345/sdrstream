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
	text, _ := rttyDecodeFramed(samples, rate, markHz, spaceHz, baud)
	return text
}

// ita2Frame locks onto start bits in an oversampled mark/space stream
// (samplesPerBit = over entries per bit) and decodes ITA2 characters.
// It also reports how many characters framed cleanly, which separates the
// right mark/space polarity from a mislocked wrong-polarity reading.
func ita2Frame(level []byte, over int) (string, int) {
	var out strings.Builder
	framed := 0
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
			framed++
		}
		i += 7 * over
	}
	text := out.String()
	// A real RTTY message is a run of readable characters. Noise can
	// produce a handful of stray ones, and the letters it invents are
	// not evenly spread over the text, so both are checked.
	printable, letters := 0, 0
	for _, r := range text {
		if r >= 0x20 && r < 0x7f {
			printable++
			if r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
				letters++
			}
		}
	}
	if printable < 6 {
		return "", 0
	}
	if letters*2 < printable {
		return "", 0
	}
	return text, framed
}

// RTTYDecodeAuto tries both mark/space polarities and returns the text of
// the better-scoring decode. HF RTTY services disagree on which tone is
// mark: amateur practice puts mark high, but the DWD weather service keys
// F1B with mark as the lower tone, so a fixed convention silently decodes
// inverted garbage on one of them.
func RTTYDecodeAuto(samples []float64, rate int, markHz, spaceHz, baud float64) (text string, inverted bool) {
	// Resolve the defaults before swapping: with zero tones both attempts
	// would otherwise re-default to the same assignment and never swap.
	if markHz == 0 {
		markHz = 2125
	}
	if spaceHz == 0 {
		spaceHz = markHz - 170
	}
	if baud <= 0 {
		baud = 45.45
	}
	direct, directN := rttyDecodeFramed(samples, rate, markHz, spaceHz, baud)
	swapped, swappedN := rttyDecodeFramed(samples, rate, spaceHz, markHz, baud)
	dq, sq := RTTYQuality(direct), RTTYQuality(swapped)
	// Quality first; on a tie prefer the polarity that framed more
	// characters. A wrong-polarity reading mislocks onto data bits and
	// skips most frames, but its text is all letters, so quality alone
	// cannot tell it apart.
	if sq > dq || sq == dq && swappedN > directN {
		return swapped, true
	}
	return direct, false
}

// rttyDecodeFramed is RTTYDecode plus the cleanly framed character count.
func rttyDecodeFramed(samples []float64, rate int, markHz, spaceHz, baud float64) (string, int) {
	if rate < 4000 || len(samples) < rate/10 {
		return "", 0
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

// RTTYQuality scores decoded RTTY text for readability. Real teleprinter
// traffic is mostly letters, digits, spaces and a small set of figures;
// noise decodes into rare punctuation runs and control characters.
func RTTYQuality(text string) float64 {
	if len(text) == 0 {
		return 0
	}
	good, bad, letters := 0, 0, 0
	for _, r := range text {
		switch {
		case r >= 'A' && r <= 'Z' || r >= '0' && r <= '9':
			good++
			letters++
		case r == ' ' || r == '\n' || r == '\r':
			good++
		case r == '.' || r == ',' || r == '-' || r == '/' || r == ':' || r == '(' || r == ')' || r == '=' || r == '+' || r == '?':
			good++
		default:
			bad++
		}
	}
	if letters < 4 {
		return 0
	}
	return 100 * float64(good) / float64(good+bad)
}
