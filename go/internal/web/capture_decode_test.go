package web

import (
	"bytes"
	"encoding/json"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/testutil/wefaxfixture"
	"github.com/chaim12345/sdrstream/go/internal/voice"
)

func decodeCapture(t *testing.T, s *Server, body string) (*httptest.ResponseRecorder, captureDecodeResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/captures/decode", strings.NewReader(body))
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	rec := httptest.NewRecorder()
	s.handleCaptureDecode(rec, req)
	var resp captureDecodeResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("response not JSON: %v (%q)", err, rec.Body.String())
		}
	}
	return rec, resp
}

func TestCaptureDecodeGuards(t *testing.T) {
	s := &Server{capturesDir: t.TempDir(), galleryDir: t.TempDir()}
	// Method.
	req := httptest.NewRequest(http.MethodGet, "/api/captures/decode", nil)
	rec := httptest.NewRecorder()
	s.handleCaptureDecode(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: got %d, want 405", rec.Code)
	}
	// Cross-site.
	req = httptest.NewRequest(http.MethodPost, "/api/captures/decode", strings.NewReader(`{"name":"x.wav"}`))
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rec = httptest.NewRecorder()
	s.handleCaptureDecode(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-site: got %d, want 403", rec.Code)
	}
	// Traversal and non-WAV.
	for _, name := range []string{"../escape.wav", "sub/dir.wav", "capture.png", ""} {
		rec, _ := decodeCapture(t, s, `{"name":"`+name+`"}`)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("name %q: got %d, want 400", name, rec.Code)
		}
	}
	// Missing capture.
	rec, _ = decodeCapture(t, s, `{"name":"CAP_gone.wav"}`)
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing: got %d, want 404", rec.Code)
	}
	// Capture without a sidecar: the decoder choice would be a guess.
	writeWAVFixture(t, s.capturesDir, "CAP_nosidecar.wav", 12000, 2)
	rec, _ = decodeCapture(t, s, `{"name":"CAP_nosidecar.wav"}`)
	if rec.Code != http.StatusConflict {
		t.Errorf("no sidecar: got %d, want 409", rec.Code)
	}
	// Sidecar mode without an image decoder.
	writeWAVFixture(t, s.capturesDir, "CAP_voice.wav", 12000, 2)
	if err := WriteCaptureMetadata(s.capturesDir, "CAP_voice.wav", CaptureMetadata{Mode: "voice"}); err != nil {
		t.Fatal(err)
	}
	rec, _ = decodeCapture(t, s, `{"name":"CAP_voice.wav"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "voice") {
		t.Errorf("voice mode: got %d %q, want 409 naming the mode", rec.Code, rec.Body.String())
	}
}

func writeWAVFixture(t *testing.T, dir, name string, rate, seconds int) []float64 {
	t.Helper()
	rng := rand.New(rand.NewSource(7))
	samples := make([]float64, rate*seconds)
	for i := range samples {
		samples[i] = 0.4 * (rng.Float64()*2 - 1)
	}
	if err := voice.WriteWAV(dir+"/"+name, samples, rate); err != nil {
		t.Fatal(err)
	}
	return samples
}

func TestCaptureDecodeRejectsNoise(t *testing.T) {
	s := &Server{capturesDir: t.TempDir(), galleryDir: t.TempDir()}
	writeWAVFixture(t, s.capturesDir, "CAP_noise.wav", 12000, 12)
	if err := WriteCaptureMetadata(s.capturesDir, "CAP_noise.wav", CaptureMetadata{Mode: "wefax"}); err != nil {
		t.Fatal(err)
	}
	rec, resp := decodeCapture(t, s, `{"name":"CAP_noise.wav"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 with honest failure (%s)", rec.Code, rec.Body.String())
	}
	if resp.OK || resp.Reason == "" {
		t.Errorf("noise decode: got %+v, want ok:false with a reason", resp)
	}
	if len(s.liveGallery) != 0 {
		t.Errorf("failed decode published %d gallery items, want 0", len(s.liveGallery))
	}
}

func TestCaptureDecodeWEFAXPublishes(t *testing.T) {
	s := &Server{capturesDir: t.TempDir(), galleryDir: t.TempDir()}
	// Gate-passing chart config from the hfrun gate tests: 280 lines
	// clears the structural image gate; bars content avoids the
	// phasing-sync slow-diagonal tracker limitation.
	samples := wefaxfixture.Encode(wefaxfixture.Fixture{Rate: 12000, ImageLines: 280, Bars: true, LineSec: 0.5004, NoiseAmp: 0.3})
	if err := voice.WriteWAV(s.capturesDir+"/CAP_fax.wav", samples, 12000); err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second)
	if err := WriteCaptureMetadata(s.capturesDir, "CAP_fax.wav", CaptureMetadata{Mode: "wefax", Receiver: "test-receiver", FrequencyHz: 13900000, Started: started}); err != nil {
		t.Fatal(err)
	}
	rec, resp := decodeCapture(t, s, `{"name":"CAP_fax.wav"}`)
	if rec.Code != http.StatusOK || !resp.OK {
		t.Fatalf("synthetic gate-passing WEFAX: got %d %+v, want 200 ok:true", rec.Code, resp)
	}
	if !strings.HasPrefix(resp.Reason, "wefax verified") {
		t.Errorf("reason %q, want the wefax verified gate diagnostics", resp.Reason)
	}
	if len(s.liveGallery) != 1 {
		t.Fatalf("published %d gallery items, want 1", len(s.liveGallery))
	}
	item := s.liveGallery[0]
	if item.Mode != "wefax" || item.Kind != "image" {
		t.Errorf("item mode/kind %q/%q, want wefax/image", item.Mode, item.Kind)
	}
	if !strings.HasPrefix(item.Verification, "wefax verified") {
		t.Errorf("item verification %q, want gate-owned verdict", item.Verification)
	}
	if item.DecodedFrom != "CAP_fax.wav" {
		t.Errorf("decoded_from %q, want the capture name", item.DecodedFrom)
	}
	if !item.Start.Equal(started) {
		t.Errorf("item start %v, want the capture's transmission time %v", item.Start, started)
	}
	if item.Station != "test-receiver" || item.FrequencyHz != 13900000 {
		t.Errorf("provenance station/freq %q/%d, want the sidecar's", item.Station, item.FrequencyHz)
	}
	// The image itself is served.
	req := httptest.NewRequest(http.MethodGet, item.URL, nil)
	rec = httptest.NewRecorder()
	s.handleGalleryFile(rec, req)
	if rec.Code != http.StatusOK || !bytes.HasPrefix(rec.Body.Bytes(), []byte{0x89, 'P', 'N', 'G'}) {
		t.Errorf("gallery image: got %d with %d bytes", rec.Code, rec.Body.Len())
	}
}
