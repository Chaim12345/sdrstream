package decode

import (
	"bytes"
	"github.com/chaim12345/sdrstream/go/internal/testutil/wefaxfixture"
	"image"
	"image/png"
	"math"
	"math/rand"
	"testing"
)

func decodeWEFAXFixture(t *testing.T, f wefaxfixture.Fixture) ([]byte, WEFAXReport) {
	t.Helper()
	samples := wefaxfixture.Encode(f)
	pngData, rep, err := WEFAXImageDetailed(samples, int(f.Rate), 120, 576)
	if err != nil {
		t.Fatalf("decode failed: %v (report %+v)", err, rep)
	}
	return pngData, rep
}

func TestWEFAXImageFixture(t *testing.T) {
	f := wefaxfixture.Fixture{Rate: 12000, PhasingLines: 60, ImageLines: 80, StartToneSec: 3}
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
	if img.Bounds().Dy() < f.ImageLines+f.PhasingLines/2 {
		t.Fatalf("height %d, want >= %d", img.Bounds().Dy(), f.ImageLines+f.PhasingLines/2)
	}
	// Phasing wedge must decode white, phasing background black.
	if w, _, _, _ := img.At(20, 10).RGBA(); w>>8 < 200 {
		t.Errorf("phasing wedge grey %d, want white", w>>8)
	}
	if b, _, _, _ := img.At(600, 10).RGBA(); b>>8 > 60 {
		t.Errorf("phasing background grey %d, want black", b>>8)
	}
	// Image region: diagonal area away from the stripe is black.
	imgY := f.PhasingLines + 5
	if b, _, _, _ := img.At(900, imgY+f.ImageLines/4).RGBA(); b>>8 > 90 {
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
	f := wefaxfixture.Fixture{Rate: 12000, PhasingLines: 60, ImageLines: 60, LineSec: 0.50075}
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
	f := wefaxfixture.Fixture{Rate: 12000, PhasingLines: 60, ImageLines: 40, NoiseAmp: 0.6}
	_, rep := decodeWEFAXFixture(t, f)
	if rep.WedgeContrast < 0.15 {
		t.Errorf("wedge contrast %.2f under noise, want >= 0.15", rep.WedgeContrast)
	}
}

func TestWEFAXToneOffsetFixture(t *testing.T) {
	// Transmitter/receiver offset: tones at 1520/2320 Hz.
	f := wefaxfixture.Fixture{Rate: 12000, PhasingLines: 60, ImageLines: 40, BlackHz: 1520, WhiteHz: 2320}
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

// wefaxBandMeans returns the mean rendered gray level at the centers
// of nb vertical bands, averaged over image rows [y0, y1). The middle
// 40% of each band is sampled, away from band edges.
func wefaxBandMeans(img image.Image, nb, y0, y1 int) []float64 {
	b := img.Bounds()
	w := b.Dx()
	if y1 > b.Dy() {
		y1 = b.Dy()
	}
	means := make([]float64, nb)
	counts := make([]int, nb)
	for y := y0; y < y1; y++ {
		for band := 0; band < nb; band++ {
			for k := 0; k < 8; k++ {
				x := int((float64(band) + 0.3 + 0.4*float64(k)/7) * float64(w) / float64(nb))
				g, _, _, _ := img.At(x, y).RGBA()
				means[band] += float64(g >> 8)
				counts[band]++
			}
		}
	}
	for band := range means {
		if counts[band] > 0 {
			means[band] /= float64(counts[band])
		}
	}
	return means
}

func TestWEFAXGradientToneMapping(t *testing.T) {
	// Intermediate gray levels ("colour" coverage): checkerboard cells
	// take one of six x-band levels, so the render must order the bands
	// dark to light with no inversion, use the full tone range, and
	// hold that alignment down the chart. Measured on this fixture
	// (2026-10-23, deterministic): band means about
	// [39 44 54 107 128 149], stable within 5 gray levels between the
	// top and bottom of the image region; the dark-end compression is
	// the two-bin ratio demod, asserted here only as ordering.
	f := wefaxfixture.Fixture{Rate: 12000, PhasingLines: 60, ImageLines: 80, StartToneSec: 3, Gradient: true, NoiseAmp: 0.15}
	pngData, rep := decodeWEFAXFixture(t, f)
	if rep.SyncSource != "phasing-wedge" {
		t.Errorf("sync source %q, want phasing-wedge", rep.SyncSource)
	}
	img, err := png.Decode(bytes.NewReader(pngData))
	if err != nil {
		t.Fatal(err)
	}
	top := wefaxBandMeans(img, 6, 70, 90)
	bottom := wefaxBandMeans(img, 6, 100, 130)
	for b := 1; b < 6; b++ {
		if top[b] < top[b-1]+3 {
			t.Errorf("band %d mean %.0f not above band %d mean %.0f; tone order broken", b, top[b], b-1, top[b-1])
		}
	}
	if top[5]-top[0] < 80 {
		t.Errorf("tone range %.0f, want >= 80 (black-to-white endpoints)", top[5]-top[0])
	}
	for b := 0; b < 6; b++ {
		if math.Abs(top[b]-bottom[b]) > 12 {
			t.Errorf("band %d mean drifted %.0f -> %.0f down-chart; alignment unstable", b, top[b], bottom[b])
		}
	}
}

func TestWEFAXMidChartCorrelationSync(t *testing.T) {
	// No phasing block (capture started mid-chart), 800 ppm clock
	// drift, moderate noise: the line-correlation path must lock.
	f := wefaxfixture.Fixture{Rate: 12000, ImageLines: 120, Bars: true, LineSec: 0.5004, NoiseAmp: 0.3}
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
	f := wefaxfixture.Fixture{Rate: 12000, PhasingLines: 60, ImageLines: 40,
		CarrierHz: 2348, CarrierAmp: 0.5}
	_, rep := decodeWEFAXFixture(t, f)
	if rep.SyncSource != "phasing-wedge" {
		t.Errorf("sync source %q, want phasing-wedge", rep.SyncSource)
	}
}
