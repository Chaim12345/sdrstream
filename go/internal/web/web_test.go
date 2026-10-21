package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/gallery"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	png := append([]byte("\x89PNG\r\n\x1a\n"), []byte("pixels")...)
	if err := os.WriteFile(filepath.Join(dir, "7-shot.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secret.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}
	return &Server{
		galleryDir: dir,
		gallery: []gallery.Item{{
			ObservationID: 7, Satellite: "ISS", Station: "test", Kind: "image",
			Name: "7-shot.png", Start: time.Unix(1_700_000_000, 0),
			Path: filepath.Join(dir, "7-shot.png"),
		}},
	}
}

func TestGalleryAPI(t *testing.T) {
	server := newTestServer(t)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/api/gallery.json", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	var items []gallery.Item
	if err := json.Unmarshal(recorder.Body.Bytes(), &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items", len(items))
	}
	if items[0].URL != "/gallery/7-shot.png" {
		t.Errorf("URL = %q", items[0].URL)
	}
	if items[0].Path != "" {
		t.Errorf("absolute path leaked: %q", items[0].Path)
	}
}

func TestGalleryFile(t *testing.T) {
	server := newTestServer(t)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/gallery/7-shot.png", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("known file: status = %d", recorder.Code)
	}
	if !strings.HasPrefix(recorder.Body.String(), "\x89PNG") {
		t.Fatal("served the wrong bytes")
	}

	for _, path := range []string{"/gallery/secret.png", "/gallery/..%2Fsecret.png", "/gallery/"} {
		recorder = httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", path, nil))
		if recorder.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", path, recorder.Code)
		}
	}
}

func TestSatellitesMarkRelays(t *testing.T) {
	server := &Server{}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/api/satellites.json", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	var sats []SatelliteInfo
	if err := json.Unmarshal(recorder.Body.Bytes(), &sats); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, sat := range sats {
		if sat.Name == "ISS" && sat.Relay == "FM voice" {
			found = true
		}
	}
	if !found {
		t.Fatal("ISS is not marked as an FM voice relay")
	}
}
