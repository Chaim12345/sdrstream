package monitor

import (
	"context"
	"math"
	"math/rand"
	"strings"
	"testing"
)

// synthRTTYMarkLow modulates text as 45.45 baud RTTY with the DWD F1B
// polarity: mark LOW (1955 Hz), space high (2125 Hz). The amateur
// convention (and the old fixed decoder default) is mark high.
func synthRTTYMarkLow(text string, rate int) []float64 {
	const markHz, spaceHz, baud = 1955.0, 2125.0, 45.45
	letters := map[byte]int{
		'E': 1, 'A': 3, ' ': 4, 'S': 5, 'I': 6, 'U': 7, 'D': 9, 'R': 10,
		'J': 11, 'N': 12, 'F': 13, 'C': 14, 'K': 15, 'T': 16, 'Z': 17,
		'L': 18, 'W': 19, 'H': 20, 'Y': 21, 'P': 22, 'Q': 23, 'O': 24,
		'B': 25, 'G': 26, 'M': 28, 'X': 29, 'V': 30,
	}
	bits := []int{1, 1, 1, 1, 1, 1, 1, 1}
	push := func(code int) {
		bits = append(bits, 0)
		for b := 0; b < 5; b++ {
			bits = append(bits, code>>uint(b)&1)
		}
		bits = append(bits, 1, 1)
	}
	push(0x1F) // letters shift
	for i := 0; i < len(text); i++ {
		if code, ok := letters[text[i]]; ok {
			push(code)
		}
	}
	spb := float64(rate) / baud
	var out []float64
	phase := 0.0
	for n, bit := range bits {
		f := spaceHz
		if bit == 1 {
			f = markHz
		}
		for i := int(float64(n) * spb); i < int(float64(n+1)*spb); i++ {
			out = append(out, 0.8*math.Sin(phase))
			phase += 2 * math.Pi * f / float64(rate)
		}
	}
	return out
}

// TestDecodeDataRTTYAutoSwapsPolarity: the live classifier must read a
// mark-low (DWD F1B convention) RTTY signal. It used to decode only with
// the fixed amateur mark-high default, producing mislocked garbage.
func TestDecodeDataRTTYAutoSwapsPolarity(t *testing.T) {
	instance := New(Options{})
	samples := synthRTTYMarkLow("RYRY CQ CQ DE DWDX DWDX PSE K", 8000)
	// A little band noise keeps the perfectly periodic synth from
	// false-locking the earlier BPSK classifier stage.
	rng := rand.New(rand.NewSource(7))
	for i := range samples {
		samples[i] += 0.12 * rng.NormFloat64()
	}
	mode, detail, ok := instance.decodeData(context.Background(), samples, 8000, 3000, 0)
	if !ok || mode != "rtty" {
		t.Fatalf("mode = %q/%q ok=%v, want rtty", mode, detail, ok)
	}
	if !strings.Contains(detail, "DWDX") {
		t.Errorf("detail = %q, want the mark-low text with DWDX", detail)
	}
}
