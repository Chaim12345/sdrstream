package web

import (
	"bytes"
	"context"
	"encoding/json"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"image/png"

	"github.com/chaim12345/sdrstream/go/internal/decode"
	"github.com/chaim12345/sdrstream/go/internal/monitor"
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
	publishWEFAXCapture(t, s, "CAP_fax.wav", "live")
	started := time.Now().UTC().Add(-3 * time.Hour)
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
	if item.Satellite == "Live reception" {
		t.Errorf("re-decode titled %q: implies fresh reception", item.Satellite)
	}
	if got := item.galleryLiveClass(); got != galleryLiveRedecode {
		t.Errorf("class %v, want galleryLiveRedecode (listed with verdict, never counted)", got)
	}
	// The render is a wide strip by design: IOC 576 lines are 1810 px
	// wide; a 280-line fixture chart is ~280 rows tall.
	if img, err := png.Decode(bytes.NewReader(mustReadGallery(t, s, item.URL))); err != nil {
		t.Fatal(err)
	} else if img.Bounds().Dx() != 1810 || img.Bounds().Dy() < 200 || img.Bounds().Dy() > 400 {
		t.Errorf("render %dx%d, want 1810 wide and ~280 tall (short fixture, not a full chart)", img.Bounds().Dx(), img.Bounds().Dy())
	}
	if d := item.Start.Sub(started); d > time.Second || d < -time.Second {
		t.Errorf("item start %v vs capture write %v: %v apart", item.Start, started, d)
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

// The endpoint sits behind the same session auth as every /api route:
// with a UI password set, an unauthenticated POST is refused before
// any decode work happens.
func TestCaptureDecodeRequiresAuth(t *testing.T) {
	t.Setenv("SDRSTREAM_UI_PASSWORD", "test-decode-password")
	s := &Server{capturesDir: t.TempDir(), galleryDir: t.TempDir()}
	req := httptest.NewRequest(http.MethodPost, "/api/captures/decode", strings.NewReader(`{"name":"CAP_fax.wav"}`))
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated POST: got %d, want 401", rec.Code)
	}
}

// A second decode of the same capture is idempotent: no duplicate
// gallery item, the recorded verdict comes back.
func TestCaptureDecodeDuplicateIsIdempotent(t *testing.T) {
	s := &Server{capturesDir: t.TempDir(), galleryDir: t.TempDir()}
	publishWEFAXCapture(t, s, "CAP_dup.wav", "live")
	_, resp := decodeCapture(t, s, `{"name":"CAP_dup.wav"}`)
	if !resp.OK {
		t.Fatalf("first decode: %+v", resp)
	}
	rec, resp := decodeCapture(t, s, `{"name":"CAP_dup.wav"}`)
	if rec.Code != http.StatusOK || !resp.OK || !strings.Contains(resp.Reason, "already published") {
		t.Errorf("duplicate decode: got %d %+v, want idempotent ok", rec.Code, resp)
	}
	if len(s.liveGallery) != 1 {
		t.Errorf("duplicate decode left %d gallery items, want 1", len(s.liveGallery))
	}
}

// A re-decode of a stored capture is not fresh reception: the feed
// lists it as historical with the gate diagnostics in its detail, but
// it never enters the verified activity count the pill renders.
func TestCaptureDecodeNotCountedAsFreshActivity(t *testing.T) {
	s := &Server{capturesDir: t.TempDir(), galleryDir: t.TempDir()}
	publishWEFAXCapture(t, s, "CAP_feed.wav", "live")
	if _, resp := decodeCapture(t, s, `{"name":"CAP_feed.wav"}`); !resp.OK {
		t.Fatalf("decode: %+v", resp)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/activity.json", nil)
	rec := httptest.NewRecorder()
	s.handleActivity(rec, req)
	var items []monitor.Activity
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatal(err)
	}
	counted := 0
	found := false
	for _, a := range items {
		if a.Verification != "" && a.Rejected == "" {
			counted++
		}
		if strings.Contains(a.Detail, "wefax verified") {
			found = true
			if !a.Historical {
				t.Errorf("re-decode feed entry is not marked historical")
			}
			if a.Verification != "" {
				t.Errorf("re-decode feed entry carries verification %q: it would count as fresh activity", a.Verification)
			}
		}
	}
	if !found {
		t.Error("re-decode missing from the feed listing")
	}
	if counted != 0 {
		t.Errorf("pill-countable entries %d, want 0 (re-decode is not fresh activity)", counted)
	}
}

// The scanner's WEFAX publication has no decode report on the
// activity, so its items keep the legacy generic path: published, but
// with NO recorded verdict (historical, conservative). This pins the
// known limitation; a global fix must re-derive it, not stumble into
// it.
func TestScannerWEFAXWithoutReportStaysUnverdicted(t *testing.T) {
	s := &Server{capturesDir: t.TempDir(), galleryDir: t.TempDir()}
	pngData := wefaxPNG(t)
	activity := monitor.Activity{ID: 1, Started: time.Now().UTC(), Kind: "wefax", Mode: "wefax", Detail: "wefax verified: legacy scanner reason string", Source: "scanner"}
	if err := s.AddLiveArtifact(activity, pngData); err != nil {
		t.Fatal(err)
	}
	if len(s.liveGallery) != 1 {
		t.Fatalf("got %d items, want 1", len(s.liveGallery))
	}
	if s.liveGallery[0].Verification != "" {
		t.Errorf("legacy scanner WEFAX gained a verdict %q without a decode report: reason strings are not verdicts", s.liveGallery[0].Verification)
	}
}

func publishWEFAXCapture(t *testing.T, s *Server, name, source string) {
	t.Helper()
	samples := wefaxfixture.Encode(wefaxfixture.Fixture{Rate: 12000, ImageLines: 280, Bars: true, LineSec: 0.5004, NoiseAmp: 0.3})
	if err := voice.WriteWAV(s.capturesDir+"/"+name, samples, 12000); err != nil {
		t.Fatal(err)
	}
	if err := WriteCaptureMetadata(s.capturesDir, name, CaptureMetadata{Mode: "wefax", Receiver: "test-receiver", FrequencyHz: 13900000, Started: time.Now().UTC().Add(-3 * time.Hour), Source: source}); err != nil {
		t.Fatal(err)
	}
}

func wefaxPNG(t *testing.T) []byte {
	t.Helper()
	samples := wefaxfixture.Encode(wefaxfixture.Fixture{Rate: 12000, ImageLines: 280, Bars: true, LineSec: 0.5004, NoiseAmp: 0.3})
	pngData, _, err := decode.WEFAXImageDetailed(samples, 12000, 120, 576)
	if err != nil {
		t.Fatal(err)
	}
	return pngData
}

func mustReadGallery(t *testing.T, s *Server, url string) []byte {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, url, nil)
	rec := httptest.NewRecorder()
	s.handleGalleryFile(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("gallery read %s: %d", url, rec.Code)
	}
	return rec.Body.Bytes()
}

// An explicit live source keeps its verdict but publishes as a
// re-decode (never counted, never titled "Live reception"); a
// synthetic capture stays on the test-fixture tab; an unknown source
// (all pre-field sidecars) publishes the passed image historically
// with no verdict recorded - unknown provenance is never upgraded.
func TestCaptureDecodeSourceClassPlacement(t *testing.T) {
	s := &Server{capturesDir: t.TempDir(), galleryDir: t.TempDir()}
	cases := []struct {
		name, source string
		wantTab      string
		wantVerdict  bool
	}{
		{"CAP_src_live.wav", "live", "live", true},
		{"CAP_src_fix.wav", "test-fixture", "test-fixture", true},
		{"CAP_src_unk.wav", "", "", true},               // unknown provenance: measured verdict kept, never counted
		{"CAP_src_ext.wav", "satnogs", "satnogs", true}, // foreign class preserved
	}
	for _, tc := range cases {
		publishWEFAXCapture(t, s, tc.name, tc.source)
		body, _ := json.Marshal(captureDecodeRequest{Name: tc.name})
		req := httptest.NewRequest(http.MethodPost, "/api/captures/decode", bytes.NewReader(body))
		req.RemoteAddr = "127.0.0.1:1"
		rec := httptest.NewRecorder()
		s.handleCaptureDecode(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", tc.name, rec.Code, rec.Body)
		}
	}
	gall := append([]LiveGalleryItem(nil), s.liveGallery...)
	for _, tc := range cases {
		var item *LiveGalleryItem
		for i := range gall {
			if gall[i].DecodedFrom == tc.name {
				item = &gall[i]
				break
			}
		}
		if item == nil {
			t.Errorf("%s not in gallery", tc.name)
			continue
		}
		if item.Source != tc.wantTab {
			t.Errorf("%s: placed on %q tab, want %q", tc.name, item.Source, tc.wantTab)
		}
		if (item.Verification != "") != tc.wantVerdict {
			t.Errorf("%s: verdict recorded %t, want %t (unknown provenance never upgraded)", tc.name, item.Verification != "", tc.wantVerdict)
		}
		if item.Satellite == "Live reception" {
			t.Errorf("%s: titled %q: implies fresh reception", tc.name, item.Satellite)
		}
		if tc.source == "live" && item.galleryLiveClass() != galleryLiveRedecode {
			t.Errorf("%s: class %v, want galleryLiveRedecode", tc.name, item.galleryLiveClass())
		}
		if tc.source == "" && item.galleryLiveClass() != galleryLiveRedecode {
			t.Errorf("%s: class %v, want galleryLiveRedecode (verdict kept, never counted)", tc.name, item.galleryLiveClass())
		}
		if tc.source != "" && tc.source != "live" && tc.source != "test-fixture" && item.galleryLiveClass() != galleryForeign {
			t.Errorf("%s: class %v, want galleryForeign", tc.name, item.galleryLiveClass())
		}
		if tc.source == "test-fixture" && !strings.Contains(item.Satellite, "synthetic") {
			t.Errorf("%s: title %q does not declare synthetic content", tc.name, item.Satellite)
		}
	}
	// And a re-decode never increments the fresh-decode pill even in a batch.
	req := httptest.NewRequest(http.MethodGet, "/api/activity.json", nil)
	rec := httptest.NewRecorder()
	s.handleActivity(rec, req)
	var feed []monitor.Activity
	if err := json.Unmarshal(rec.Body.Bytes(), &feed); err != nil {
		t.Fatal(err)
	}
	counted := 0
	for _, a := range feed {
		if a.Verification != "" && a.Rejected == "" {
			counted++
		}
	}
	if counted != 0 {
		t.Errorf("pill-countable entries %d after 4 re-decodes, want 0", counted)
	}
}

// A queued decode whose caller goes away cancels promptly instead of
// waiting behind a running decode and publishing for nobody.
func TestCaptureDecodeQueuedCancelIsPrompt(t *testing.T) {
	s := &Server{capturesDir: t.TempDir(), galleryDir: t.TempDir()}
	publishWEFAXCapture(t, s, "CAP_q.wav", "live")
	// Hold the decode semaphore: the queued request must wait. Its
	// caller is already gone, so the handler must return promptly
	// instead of waiting for the lock and decoding for nobody.
	s.decodeMu.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	body, _ := json.Marshal(captureDecodeRequest{Name: "CAP_q.wav"})
	req := httptest.NewRequest(http.MethodPost, "/api/captures/decode", bytes.NewReader(body)).WithContext(ctx)
	req.RemoteAddr = "127.0.0.1:1"
	cancel()
	start := time.Now()
	s.handleCaptureDecode(httptest.NewRecorder(), req)
	elapsed := time.Since(start)
	s.decodeMu.Unlock()
	if elapsed > 200*time.Millisecond {
		t.Errorf("queued decode with a cancelled caller took %s; want prompt return, not a wait for the lock", elapsed)
	}
	if len(s.liveGallery) != 0 {
		t.Errorf("cancelled queued decode published %d gallery items, want 0", len(s.liveGallery))
	}
	if _, err := os.Stat(filepath.Join(s.capturesDir, "CAP_q.png")); !os.IsNotExist(err) {
		t.Errorf("cancelled queued decode left a PNG: %v", err)
	}
}

// The captures listing carries the sidecar's explicit source class so
// the UI can label provenance: live stays live, synthetic stays a test
// fixture, and a pre-field sidecar reports no source (never guessed).
func TestCapturesListCarriesSourceClass(t *testing.T) {
	s := &Server{capturesDir: t.TempDir(), galleryDir: t.TempDir()}
	publishWEFAXCapture(t, s, "CAP_src_a.wav", "live")
	publishWEFAXCapture(t, s, "CAP_src_b.wav", "test-fixture")
	publishWEFAXCapture(t, s, "CAP_src_c.wav", "")
	got := map[string]string{}
	for _, c := range s.listCaptures() {
		got[c.Path] = c.Source
	}
	if got["CAP_src_a.wav"] != "live" {
		t.Errorf("live capture source %q, want live", got["CAP_src_a.wav"])
	}
	if got["CAP_src_b.wav"] != "test-fixture" {
		t.Errorf("fixture capture source %q, want test-fixture", got["CAP_src_b.wav"])
	}
	if v, ok := got["CAP_src_c.wav"]; !ok || v != "" {
		t.Errorf("pre-field capture source %q (present %t), want empty - provenance is never guessed", v, ok)
	}
}

// A sidecar saved with CRLF line endings (copied from another machine)
// must still report its source class, not "source not recorded".
func TestCapturesListReadsCRLFSidecar(t *testing.T) {
	s := &Server{capturesDir: t.TempDir(), galleryDir: t.TempDir()}
	publishWEFAXCapture(t, s, "CAP_crlf.wav", "live")
	p := filepath.Join(s.capturesDir, "CAP_crlf.txt")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(strings.ReplaceAll(string(b), "\n", "\r\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range s.listCaptures() {
		if c.Path == "CAP_crlf.wav" && c.Source != "live" {
			t.Fatalf("CRLF sidecar source %q, want live", c.Source)
		}
	}
}
