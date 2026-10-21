package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/directory"
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

// TestIndexPageTabsShowPanel pins the tab wiring: the click handler used to
// add "active" to the clicked button twice, so every panel stayed hidden.
// The page is a string with no build step, so this is the cheapest way to
// keep that regression from coming back.
func TestIndexPageTabsShowPanel(t *testing.T) {
	if !strings.Contains(indexPage, "el('panel-' + button.dataset.tab).classList.add('active')") {
		t.Fatal("the tab handler does not show the panel it selects")
	}
	if !strings.Contains(indexPage, "aria-selected") {
		t.Fatal("the tab handler does not update aria-selected")
	}
}

// TestStationsFailureIsNotEmptiness checks that a directory that cannot be
// reached is a 502, not a 200 with an empty list: the page shows "no
// stations" for the latter, which hides a broken network behind a lie.
func TestStationsFailureIsNotEmptiness(t *testing.T) {
	server := &Server{fetchStations: func(context.Context) ([]directory.Station, error) {
		return nil, errors.New("directory offline")
	}}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/api/stations.json", nil))
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", recorder.Code)
	}
}

// TestStationsEmptyDirectory checks the other half: a directory that answered
// with no receivers is a normal, empty list.
func TestStationsEmptyDirectory(t *testing.T) {
	server := &Server{fetchStations: func(context.Context) ([]directory.Station, error) {
		return []directory.Station{}, nil
	}}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/api/stations.json", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if got := strings.TrimSpace(recorder.Body.String()); got != "[]" {
		t.Errorf("body = %q, want an empty list", got)
	}
}

// TestStationsServeStaleCache checks that an expired cache is still served
// when the refresh fails, marked stale, so a temporary outage does not empty
// the table.
func TestStationsServesStaleCache(t *testing.T) {
	server := &Server{
		stations: []directory.Station{{
			Type: "KiwiSDR", Name: "test receiver", URL: "http://example.test:8073/",
			Latitude: 51.5, Longitude: -0.1,
		}},
		stationsAt: time.Now().Add(-2 * time.Hour),
		fetchStations: func(context.Context) ([]directory.Station, error) {
			return nil, errors.New("directory offline")
		},
	}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/api/stations.json", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want the cached list", recorder.Code)
	}
	if got := recorder.Header().Get("X-Stations-Stale"); got != "true" {
		t.Errorf("X-Stations-Stale = %q, want true", got)
	}
	if !strings.Contains(recorder.Body.String(), "test receiver") {
		t.Errorf("body = %q, want the cached station", recorder.Body.String())
	}
}
