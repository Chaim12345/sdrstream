package decode

import (
	"bytes"
	"image/png"
	"math"
	"math/rand"
	"testing"
)

// wefaxFixture describes a synthesized F1C transmission.
type wefaxFixture struct {
	rate         float64
	phasingLines int
	imageLines   int
	blackHz      float64 // 0 -> 1500
	whiteHz      float64 // 0 -> 2300
	lineSec      float64 // 0 -> 0.5 (set off-nominal to simulate clock drift)
	noiseAmp     float64 // additive white noise amplitude
	startToneSec float64 // 300 Hz IOC-576 start tone before phasing
	bars         bool    // image content: horizontal bars (mid-chart captures)
	carrierHz    float64 // steady carrier near the white tone (beat test)
	carrierAmp   float64
}

// encodeWEFAX synthesizes F1C audio: an optional start tone, a phasing
// block (black lines with a 5% white leading wedge), then image lines
// with a diagonal test pattern plus a checkerboard band.
func encodeWEFAX(f wefaxFixture) []float64 {
	if f.rate == 0 {
		f.rate = 12000
	}
	if f.blackHz == 0 {
		f.blackHz = 1500
	}
	if f.whiteHz == 0 {
		f.whiteHz = 2300
	}
	if f.lineSec == 0 {
		f.lineSec = 0.5
	}
	lineSamples := int(f.lineSec * f.rate)
	n := int(f.startToneSec*f.rate) + (f.phasingLines+f.imageLines)*lineSamples
	out := make([]float64, n)
	rng := rand.New(rand.NewSource(42))
	phase := 0.0
	startToneN := int(f.startToneSec * f.rate)
	for i := 0; i < n; i++ {
		freq := 300.0
		if i >= startToneN {
			k := i - startToneN
			line := k / lineSamples
			inLine := k % lineSamples
			frac := float64(inLine) / float64(lineSamples)
			grey := 0.0
			switch {
			case line < f.phasingLines:
				if frac < 0.05 {
					grey = 1.0 // phasing wedge
				}
			default:
				il := line - f.phasingLines
				px := frac
				if f.bars {
					// Horizontal bars 3 lines tall every 7 lines at
					// varying x extents: chart-like content with no
					// periodic white wedge.
					if il%7 < 3 && px > 0.1+0.3*math.Abs(math.Sin(float64(il/7))) && px < 0.8 {
						grey = 1.0
					}
				} else if il < f.imageLines/2 {
					// Diagonal stripe over the upper half, checkerboard below.
					if math.Abs(px-float64(il)/float64(f.imageLines/2)) < 0.08 {
						grey = 1.0
					}
				} else {
					if (int(px*20)+il)%2 == 0 {
						grey = 1.0
					}
				}
			}
			freq = f.blackHz + grey*(f.whiteHz-f.blackHz)
		}
		phase += 2 * math.Pi * freq / f.rate
		out[i] = 0.6*math.Sin(phase) + f.noiseAmp*(rng.Float64()*2-1)
	}
	if f.carrierHz > 0 {
		for i := range out {
			out[i] += f.carrierAmp * math.Sin(2*math.Pi*f.carrierHz*float64(i)/f.rate)
		}
	}
	return out
}

func decodeWEFAXFixture(t *testing.T, f wefaxFixture) ([]byte, WEFAXReport) {
	t.Helper()
	samples := encodeWEFAX(f)
	pngData, rep, err := WEFAXImageDetailed(samples, int(f.rate), 120, 576)
	if err != nil {
		t.Fatalf("decode failed: %v (report %+v)", err, rep)
	}
	return pngData, rep
}

func TestWEFAXImageFixture(t *testing.T) {
	f := wefaxFixture{rate: 12000, phasingLines: 60, imageLines: 80, startToneSec: 3}
	pngData, rep := decodeWEFAXFixture(t, f)
	img, err := png.Decode(bytes.NewReader(pngData))
	if err != nil {
		t.Fatal(err)
	}
	wantW := 1810 // IOC 576 * pi
	if img.Bounds().Dx() != wantW {
		t.Fatalf("width %d, want %d", img.Bounds().Dx(), wantW)
	}
	// The render starts inside the phasing block and loses a few edge
	// lines; require most of the transmission to render.
	if img.Bounds().Dy() < f.imageLines+f.phasingLines/2 {
		t.Fatalf("height %d, want >= %d", img.Bounds().Dy(), f.imageLines+f.phasingLines/2)
	}
	// Phasing wedge must decode white, phasing background black.
	if w, _, _, _ := img.At(20, 10).RGBA(); w>>8 < 200 {
		t.Errorf("phasing wedge grey %d, want white", w>>8)
	}
	if b, _, _, _ := img.At(600, 10).RGBA(); b>>8 > 60 {
		t.Errorf("phasing background grey %d, want black", b>>8)
	}
	// Image region: diagonal area away from the stripe is black.
	imgY := f.phasingLines + 5
	if b, _, _, _ := img.At(900, imgY+f.imageLines/4).RGBA(); b>>8 > 90 {
		t.Errorf("image background grey %d, want near black", b>>8)
	}
	if math.Abs(rep.BlackHz-1500) > 15 || math.Abs(rep.WhiteHz-2300) > 15 {
		t.Errorf("tone lock %.0f/%.0f Hz, want 1500/2300", rep.BlackHz, rep.WhiteHz)
	}
	if math.Abs(rep.DriftPPM) > 200 {
		t.Errorf("drift %.0f ppm on a clean fixture, want ~0", rep.DriftPPM)
	}
	if rep.AntiCorrelation > -0.5 {
		t.Errorf("anti-correlation %.2f, want strongly negative for FSK", rep.AntiCorrelation)
	}
}

func TestWEFAXSlantFixture(t *testing.T) {
	// Receiver clock 1500 ppm fast: 0.50075 s lines instead of 0.5 s.
	f := wefaxFixture{rate: 12000, phasingLines: 60, imageLines: 60, lineSec: 0.50075}
	pngData, rep := decodeWEFAXFixture(t, f)
	if rep.DriftPPM < 1200 || rep.DriftPPM > 1800 {
		t.Errorf("fitted drift %.0f ppm, want ~1500", rep.DriftPPM)
	}
	img, err := png.Decode(bytes.NewReader(pngData))
	if err != nil {
		t.Fatal(err)
	}
	// With slant corrected the wedge still renders white late in the
	// phasing block; a fixed clock would have walked off it.
	if w, _, _, _ := img.At(20, 50).RGBA(); w>>8 < 200 {
		t.Errorf("late phasing wedge grey %d, want white (slant uncorrected?)", w>>8)
	}
}

func TestWEFAXNoisyFixture(t *testing.T) {
	// Amplitude 0.6 signal with 0.6 white noise: roughly 0 dB SNR,
	// worse than the Bonn capture but wideband.
	f := wefaxFixture{rate: 12000, phasingLines: 60, imageLines: 40, noiseAmp: 0.6}
	_, rep := decodeWEFAXFixture(t, f)
	if rep.WedgeContrast < 0.15 {
		t.Errorf("wedge contrast %.2f under noise, want >= 0.15", rep.WedgeContrast)
	}
}

func TestWEFAXToneOffsetFixture(t *testing.T) {
	// Transmitter/receiver offset: tones at 1520/2320 Hz.
	f := wefaxFixture{rate: 12000, phasingLines: 60, imageLines: 40, blackHz: 1520, whiteHz: 2320}
	_, rep := decodeWEFAXFixture(t, f)
	if math.Abs(rep.BlackHz-1520) > 15 || math.Abs(rep.WhiteHz-2320) > 15 {
		t.Errorf("tone lock %.0f/%.0f Hz, want 1520/2320", rep.BlackHz, rep.WhiteHz)
	}
}

func TestWEFAXRejectsNoise(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	samples := make([]float64, 20*12000)
	for i := range samples {
		samples[i] = rng.Float64()*2 - 1
	}
	if _, err := WEFAXImage(samples, 12000, 120, 576); err == nil {
		t.Fatal("pure noise decoded as a fax image")
	}
}

func TestWEFAXRejectsConstantTone(t *testing.T) {
	samples := make([]float64, 20*12000)
	for i := range samples {
		samples[i] = 0.6 * math.Sin(2*math.Pi*1900*float64(i)/12000)
	}
	if _, err := WEFAXImage(samples, 12000, 120, 576); err == nil {
		t.Fatal("constant carrier decoded as a fax image")
	}
}

func TestWEFAXRejectsShort(t *testing.T) {
	if _, err := WEFAXImage(make([]float64, 4000), 12000, 120, 576); err == nil {
		t.Fatal("short recording accepted")
	}
}

func TestWEFAXMidChartCorrelationSync(t *testing.T) {
	// No phasing block (capture started mid-chart), 800 ppm clock
	// drift, moderate noise: the line-correlation path must lock.
	f := wefaxFixture{rate: 12000, imageLines: 120, bars: true, lineSec: 0.5004, noiseAmp: 0.3}
	_, rep := decodeWEFAXFixture(t, f)
	if rep.SyncSource != "line-correlation" {
		t.Errorf("sync source %q, want line-correlation", rep.SyncSource)
	}
	if rep.DriftPPM < 500 || rep.DriftPPM > 1100 {
		t.Errorf("fitted drift %.0f ppm, want ~800", rep.DriftPPM)
	}
}

func TestWEFAXCarrierBeatFixture(t *testing.T) {
	// A steady carrier 48 Hz above the white tone writes a 48 Hz beat
	// into the white envelope. The decoder must notch it and still
	// lock the phasing wedge.
	f := wefaxFixture{rate: 12000, phasingLines: 60, imageLines: 40,
		carrierHz: 2348, carrierAmp: 0.5}
	_, rep := decodeWEFAXFixture(t, f)
	if rep.SyncSource != "phasing-wedge" {
		t.Errorf("sync source %q, want phasing-wedge", rep.SyncSource)
	}
}
