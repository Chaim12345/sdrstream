package web

import (
	"bytes"
	"image/png"
	"math"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/chaim12345/sdrstream/go/internal/common"
)

// A 700 Hz tone at 7119 Hz must peak in the spectrogram row band that
// corresponds to 700 Hz, and the PNG must parse.
func TestSpectrogramToneLandsInRightBin(t *testing.T) {
	rate := 7119
	n := rate * 2
	samples := make([]int16, n)
	for i := range samples {
		samples[i] = int16(12000 * math.Sin(2*math.Pi*700*float64(i)/float64(rate)))
	}
	pngBytes, err := spectrogramPNG(samples, rate)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		t.Fatalf("decode PNG: %v", err)
	}
	if img.Bounds().Dy() != specHeight {
		t.Fatalf("height = %d, want %d", img.Bounds().Dy(), specHeight)
	}
	// Column-average energy per row; the brightest row must sit within
	// one row of the 700 Hz bin (rows run top = Nyquist to bottom = 0).
	bestRow, bestVal := 0, -1.0
	for y := 0; y < specHeight; y++ {
		sum := 0.0
		for x := 0; x < img.Bounds().Dx(); x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			sum += float64(r+g+b) / 3
		}
		if sum > bestVal {
			bestVal, bestRow = sum, y
		}
	}
	bins := specFFT / 2
	wantBin := 700.0 / (float64(rate) / 2) * float64(bins)
	wantRow := specHeight - 1 - int(float64(specHeight)*wantBin/float64(bins))
	if d := bestRow - wantRow; d < -2 || d > 2 {
		t.Fatalf("brightest row %d, want near %d (700 Hz)", bestRow, wantRow)
	}
}

// The /audio/<name>.wav.spec.png route generates from the archived WAV,
// serves image/png, and caches beside the clip; a missing WAV is a 404
// rather than a broken image.
func TestSpectrogramRouteGeneratesServesCaches(t *testing.T) {
	dir := t.TempDir()
	rate := 8000
	samples := make([]int16, rate)
	for i := range samples {
		samples[i] = int16(9000 * math.Sin(2*math.Pi*440*float64(i)/float64(rate)))
	}
	if err := common.WriteWAVFile(filepath.Join(dir, "clip_20261023T000000Z_1.wav"), samples, rate); err != nil {
		t.Fatal(err)
	}
	s := &Server{capturesDir: dir}
	s.NoAuth = true

	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest("GET", "/audio/clip_20261023T000000Z_1.wav.spec.png", nil))
	if rr.Code != 200 || rr.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("spectrogram = %d %q", rr.Code, rr.Header().Get("Content-Type"))
	}
	if !bytes.HasPrefix(rr.Body.Bytes(), []byte("\x89PNG")) {
		t.Fatalf("not a PNG: %d bytes", rr.Body.Len())
	}
	if _, err := os.Stat(filepath.Join(dir, "clip_20261023T000000Z_1.spec.png")); err != nil {
		t.Fatalf("no cache written: %v", err)
	}

	// Missing audio: honest 404, not a 200 with garbage.
	rr = httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest("GET", "/audio/gone.wav.spec.png", nil))
	if rr.Code != 404 {
		t.Fatalf("missing clip spectrogram = %d, want 404", rr.Code)
	}
}

// A sidecar whose WAV is gone still lists as a capture with no audio URL,
// and it must not shadow the real WAV records.
func TestSidecarOnlyCaptureListedWithoutAudio(t *testing.T) {
	dir := t.TempDir()
	if err := common.WriteWAVFile(filepath.Join(dir, "DATA_7074kHz_ft8_kiwi_test_8073_20261023T000000Z_1.wav"), []int16{1, 2, 3}, 8000); err != nil {
		t.Fatal(err)
	}
	if err := WriteCaptureMetadata(dir, "DATA_7074kHz_ft8_kiwi_test_8073_20261023T000000Z_1.wav", CaptureMetadata{
		Mode: "ft8", Receiver: "kiwi.test:8073", Detail: "CQ DL1ABC JO31", Verified: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "NAVTEX_490kHz_navtex_rx_20261023T001000Z_gone.txt"),
		[]byte("frequency_hz=490000\nmode=navtex\nreceiver=websdr.test:8901\ndecode=ZCZC BA31 SURVIVING TEXT\nkind=data\nverified=false\ngate=text only\nstarted=2026-10-23T00:10:00Z\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Server{capturesDir: dir}
	got := s.listCaptures()
	if len(got) != 2 {
		t.Fatalf("listCaptures = %d, want 2", len(got))
	}
	var textOnly *Capture
	for i := range got {
		if got[i].URL == "" {
			textOnly = &got[i]
		}
	}
	if textOnly == nil {
		t.Fatalf("sidecar-only record missing: %+v", got)
	}
	if textOnly.Decode != "ZCZC BA31 SURVIVING TEXT" || textOnly.Verified {
		t.Fatalf("text-only record = %+v", textOnly)
	}
}
