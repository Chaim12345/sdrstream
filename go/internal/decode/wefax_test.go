package decode

import (
	"bytes"
	"image/png"
	"math"
	"testing"
)

// encodeWEFAX synthesizes F1C audio: 120 LPM, each line starting with a
// white phasing wedge (first 5%), then a diagonal test pattern.
func encodeWEFAX(rate float64, lines, pixelsPerLine int) []float64 {
	lineSamples := int(rate * 0.5)
	n := lines * lineSamples
	out := make([]float64, n)
	phase := 0.0
	for i := 0; i < n; i++ {
		line := i / lineSamples
		inLine := i % lineSamples
		grey := 0.0
		switch {
		case inLine < lineSamples/20:
			grey = 1.0 // phasing wedge: white
		default:
			px := float64(inLine) / float64(lineSamples)
			if (px-float64(line)/float64(lines)) > 0.5 {
				grey = 1.0
			}
		}
		freq := 1500 + grey*800
		phase += 2 * math.Pi * freq / rate
		out[i] = 0.7 * math.Sin(phase)
	}
	return out
}

func TestWEFAXImageFixture(t *testing.T) {
	const rate = 12000
	lines, ppl := 40, 1152
	samples := encodeWEFAX(rate, lines, ppl)
	pngData, err := WEFAXImage(samples, rate, 120, 576)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(pngData))
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != 1152 || img.Bounds().Dy() < lines-1 {
		t.Fatalf("bounds %v", img.Bounds())
	}
	// Phasing wedge must decode white, deep in the pattern must decode
	// near black/white extremes, not a grey wash.
	wedge, _, _, _ := img.At(10, 5).RGBA()
	if wedge>>8 < 200 {
		t.Errorf("phasing wedge grey %d, want white", wedge>>8)
	}
}

func TestWEFAXRejectsShort(t *testing.T) {
	if _, err := WEFAXImage(make([]float64, 4000), 12000, 120, 576); err == nil {
		t.Fatal("short recording accepted")
	}
}
