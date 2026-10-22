package decode

import (
	"math"
	"strings"
	"testing"
)

// synthReviewRTTY modulates text as 45.45 baud RTTY (ITA2, one start bit,
// five data bits LSB first, stop bits) at the given mark/space tones.
func synthReviewRTTY(text string, rate int, markHz, spaceHz, baud float64) []float64 {
	letters := map[byte]int{
		'E': 1, 'A': 3, ' ': 4, 'S': 5, 'I': 6, 'U': 7, 'D': 9, 'R': 10,
		'J': 11, 'N': 12, 'F': 13, 'C': 14, 'K': 15, 'T': 16, 'Z': 17,
		'L': 18, 'W': 19, 'H': 20, 'Y': 21, 'P': 22, 'Q': 23, 'O': 24,
		'B': 25, 'G': 26, 'M': 28, 'X': 29, 'V': 30,
	}
	var bits []int
	bits = append(bits, 1, 1, 1, 1, 1, 1, 1, 1) // idle mark
	push := func(code int) {
		bits = append(bits, 0) // start
		for b := 0; b < 5; b++ {
			bits = append(bits, code>>uint(b)&1)
		}
		bits = append(bits, 1, 1) // stop + idle
	}
	figures := map[byte]int{
		'3': 1, '-': 3, '8': 6, '7': 7, '4': 10, '!': 13, '5': 16,
		'2': 19, '#': 20, '6': 21, '0': 22, '1': 23, '9': 24, '.': 28,
		'/': 29, ';': 30,
	}
	inFigures := false
	push(0x1F) // letters shift
	for i := 0; i < len(text); i++ {
		code, isLetter := letters[text[i]]
		if !isLetter {
			code, isLetter = figures[text[i]]
			if !isLetter {
				continue
			}
			if !inFigures {
				push(0x1B) // figures shift
				inFigures = true
			}
			push(code)
			continue
		}
		if inFigures {
			push(0x1F)
			inFigures = false
		}
		push(code)
	}
	spb := float64(rate) / baud
	out := make([]float64, 0, int(float64(len(bits))*spb)+1)
	phase := 0.0
	for n, bit := range bits {
		f := spaceHz
		if bit == 1 {
			f = markHz
		}
		// Cumulative boundaries keep rounding drift from smearing the
		// bit centres over a long message.
		for i := int(float64(n) * spb); i < int(float64(n+1)*spb); i++ {
			out = append(out, 0.8*math.Sin(phase))
			phase += 2 * math.Pi * f / float64(rate)
		}
	}
	return out
}

// TestRTTYAutoSwapDefaultPolarity: a mark-low (DWD F1B style) signal
// decodes to garbage or nothing with the fixed mark-high default, while
// the auto decoder recovers the text. The fixed-polarity default was the
// review finding; monitor and the scan command now default to auto.
func TestRTTYAutoSwapDefaultPolarity(t *testing.T) {
	const rate = 8000
	// Mark low, space high: the DWD convention, inverted from the amateur
	// default of mark 2125 / space 1955.
	samples := synthReviewRTTY("RYRY CQ CQ DE DWDX DWDX PSE K", rate, 1955, 2125, 45.45)

	fixed := RTTYDecode(samples, rate, 0, 0, 0)
	if strings.Contains(fixed, "DWDX") {
		t.Fatalf("fixed-polarity decode unexpectedly read inverted text: %q", fixed)
	}

	text, inverted := RTTYDecodeAuto(samples, rate, 0, 0, 0)
	if !inverted {
		t.Errorf("auto decode did not flag the swapped polarity, text %q", text)
	}
	if !strings.Contains(text, "DWDX") {
		t.Errorf("auto decode text %q, want it to contain DWDX", text)
	}
}
