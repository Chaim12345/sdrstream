package decode

import (
	"math"
	"strings"
	"testing"
)

// keyTwoToneCW synthesises MCW where consecutive key-down elements
// alternate between two tones, the way numbers-station Morse (the Conet
// M1 family) keys two carriers. A single-tone envelope only sees every
// other element, so decoding it exercises the two-tone fallback.
func keyTwoToneCW(text string, toneA, toneB float64, dotMs int, rate int) []float64 {
	dot := rate * dotMs / 1000
	var out []float64
	appendTone := func(n int, hz float64) {
		for i := 0; i < n; i++ {
			out = append(out, 0.6*math.Sin(2*math.Pi*hz*float64(i)/float64(rate)))
		}
	}
	appendSilence := func(n int) {
		out = append(out, make([]float64, n)...)
	}
	tone := toneA
	swap := func() {
		if tone == toneA {
			tone = toneB
		} else {
			tone = toneA
		}
	}
	out = append(out, make([]float64, rate/2)...)
	for wi, word := range strings.Fields(text) {
		if wi > 0 {
			appendSilence(4 * dot) // word gap beyond the char gap
		}
		for ci, ch := range word {
			if ci > 0 {
				appendSilence(2 * dot) // char gap beyond the element gap
			}
			var pat string
			for k, v := range morseTable {
				if v == string(ch) {
					pat = k
					break
				}
			}
			for _, el := range pat {
				if el == '.' {
					appendTone(dot, tone)
				} else {
					appendTone(3*dot, tone)
				}
				swap()
				appendSilence(dot)
			}
		}
	}
	out = append(out, make([]float64, rate/2)...)
	return out
}

// TestMorseTwoTone verifies the two-tone MCW fallback: keyed elements
// split across two carriers must still read as text.
func TestMorseTwoTone(t *testing.T) {
	rate := 8000
	samples := keyTwoToneCW("CQ DE M1B 579", 700, 2100, 60, rate)
	result, tone, _ := morseFromSamples(samples, rate)
	if !strings.Contains(result.Text, "M1B") {
		t.Errorf("two-tone text = %q, want it to contain M1B", result.Text)
	}
	if tone <= 0 {
		t.Error("no tone reported")
	}
}

// TestMorseGapBleed keeps the archival-capture gap fix honest: gaps
// shrunk by envelope bleed must still split characters.
func TestMorseGapBleed(t *testing.T) {
	rate := 8000
	// VARC practice-recording timing (10 ms envelope units): dots 10,
	// dashes 27-31, element gaps 7, character gaps 26, word gaps 34. The
	// textbook 3-dot character gap arrives measuring barely over two.
	dotU, dashU, elGap, chGap, wordGap := 10, 29, 7, 26, 34
	win := rate / 100
	var out []float64
	on := func(units int) {
		for i := 0; i < units*win; i++ {
			out = append(out, 0.6*math.Sin(2*math.Pi*700*float64(i)/float64(rate)))
		}
	}
	off := func(units int) { out = append(out, make([]float64, units*win)...) }
	off(12)
	for wi, word := range []string{"CQ", "CQ", "DE", "M0HCL", "K"} {
		if wi > 0 {
			off(wordGap)
		}
		for ci, ch := range word {
			if ci > 0 {
				off(chGap)
			}
			var pat string
			for k, v := range morseTable {
				if v == string(ch) {
					pat = k
					break
				}
			}
			for ei, el := range pat {
				if ei > 0 {
					off(elGap)
				}
				if el == '.' {
					on(dotU)
				} else {
					on(dashU)
				}
			}
		}
	}
	off(12)
	result, _, _ := morseFromSamples(out, rate)
	if strings.Contains(result.Text, "<") {
		t.Errorf("gap-bleed decode left unknowns: %q", result.Text)
	}
	for _, want := range []string{"CQ", "M0HCL"} {
		if !strings.Contains(result.Text, want) {
			t.Errorf("gap-bleed text = %q, want %s", result.Text, want)
		}
	}
}
