package monitor

import (
	"bytes"
	"context"
	"image/png"
	"math"
	"math/rand"
	"testing"

	"github.com/chaim12345/sdrstream/go/internal/decode"
	"github.com/chaim12345/sdrstream/go/internal/testutil/sstvfixture"
)

// Live SSTV classification: a real VIS header on a decodable mode is
// enough to classify, and the job path then decodes the frame with
// per-line sync evidence for the publication gate. Voice and noise
// must never classify as SSTV.
func TestClassifySSTVReferenceFrame(t *testing.T) {
	samples, rate, err := decode.ReadWAV("../../../tests/data/ref_sstv.wav")
	if err != nil {
		t.Fatalf("read SSTV reference: %v", err)
	}
	instance := New(Options{})
	kind, mode, detail, _ := instance.classify(context.Background(), samples, rate, 0)
	if kind != "sstv" || mode != "sstv" {
		t.Fatalf("SSTV reference classified %q/%q (%q), want sstv/sstv", kind, mode, detail)
	}
	if detail == "" {
		t.Fatal("SSTV classification must name the mode and VIS")
	}
}

func TestClassifyVoiceAndNoiseAreNotSSTV(t *testing.T) {
	voice, voiceRate, err := decode.ReadWAV("../../../tests/data/real_volmet_shannon_5505_usb.wav")
	if err != nil {
		t.Fatalf("read VOLMET recording: %v", err)
	}
	instance := New(Options{})
	kind, _, detail, _ := instance.classify(context.Background(), voice, voiceRate, 0)
	if kind == "sstv" {
		t.Fatalf("VOLMET voice classified as SSTV (%q)", detail)
	}
	// Broadband noise has no VIS header either.
	rng := rand.New(rand.NewSource(42))
	noise := make([]float64, 22050*5)
	for i := range noise {
		noise[i] = rng.NormFloat64() * 0.4
	}
	if hits := decode.SSTVDetect(noise, 22050); len(hits) > 0 {
		t.Fatalf("white noise produced a VIS detect: %+v", hits[0])
	}
	// A pure 1900 Hz leader with no valid VIS bits is not enough.
	tone := make([]float64, 22050*3)
	for i := range tone {
		tone[i] = 0.5 * math.Sin(2*math.Pi*1900*float64(i)/22050)
	}
	if hits := decode.SSTVDetect(tone, 22050); len(hits) > 0 {
		t.Fatalf("bare 1900 Hz tone produced a VIS detect: %+v", hits[0])
	}
}

// A synthesized full Martin M1 frame must classify as SSTV and decode
// end to end: every line sync locked, no coasting, and pixels close to
// the encoded pattern. This is the geometry evidence verify.SSTVImage
// gates publication on.
func TestSSTVSynthesizedFrameDecodes(t *testing.T) {
	pattern := func(x, y int) (r, g, b uint8) {
		return uint8(x * 255 / (sstvfixture.Width - 1)),
			uint8(y * 255 / (sstvfixture.Height - 1)),
			uint8((x + y) * 255 / (sstvfixture.Width + sstvfixture.Height - 2))
	}
	samples := sstvfixture.EncodeMartinM1(pattern)
	hits := decode.SSTVDetect(samples, sstvfixture.Rate)
	if len(hits) != 1 || hits[0].VIS != 0x2C {
		t.Fatalf("detect on synthesized frame: %+v, want one Martin M1 hit", hits)
	}
	instance := New(Options{})
	kind, mode, detail, _ := instance.classify(context.Background(), samples, sstvfixture.Rate, 0)
	if kind != "sstv" || mode != "sstv" {
		t.Fatalf("synthesized frame classified %q/%q (%q), want sstv/sstv", kind, mode, detail)
	}
	data, rep, err := decode.SSTVImageDetailed(samples, sstvfixture.Rate)
	if err != nil {
		t.Fatalf("full synthesized frame does not decode: %v", err)
	}
	if rep.Width != sstvfixture.Width || rep.Height != sstvfixture.Height {
		t.Fatalf("geometry %dx%d, want %dx%d", rep.Width, rep.Height, sstvfixture.Width, sstvfixture.Height)
	}
	if rep.DecodedLines != sstvfixture.Height || rep.CoastedLines != 0 {
		t.Fatalf("decoded %d lines with %d coasted, want %d locked, 0 coasted",
			rep.DecodedLines, rep.CoastedLines, sstvfixture.Height)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode returned invalid PNG: %v", err)
	}
	// Interior comparison: FM demod edges smear by ~1 ms, so skip the
	// outer 4 pixels and allow a modest per-pixel error.
	var sumErr, maxErr float64
	count := 0
	for y := 4; y < sstvfixture.Height-4; y++ {
		for x := 4; x < sstvfixture.Width-4; x++ {
			er, eg, eb := pattern(x, y)
			pr, pg, pb, _ := img.At(x, y).RGBA()
			for _, d := range [3]float64{
				math.Abs(float64(er) - float64(pr>>8)),
				math.Abs(float64(eg) - float64(pg>>8)),
				math.Abs(float64(eb) - float64(pb>>8)),
			} {
				sumErr += d
				if d > maxErr {
					maxErr = d
				}
				count++
			}
		}
	}
	meanErr := sumErr / float64(count)
	t.Logf("pixel fidelity: mean error %.2f/255, max %.0f/255 over %d samples", meanErr, maxErr, count)
	if meanErr > 4 || maxErr > 48 {
		t.Fatalf("decoded image diverges from the encoded pattern: mean %.2f, max %.0f", meanErr, maxErr)
	}
}
