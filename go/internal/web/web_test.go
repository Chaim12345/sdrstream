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
	"github.com/chaim12345/sdrstream/go/internal/monitor"
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

// TestIndexPageViews pin the view wiring: every nav button must name a view
// that exists as a section, and the page must ship its stylesheet, script
// and the vendored map library. The page is embedded with no build step, so
// this is the cheapest way to keep a markup/behaviour drift from shipping.
func TestIndexPageViews(t *testing.T) {
	for _, view := range []string{"overview", "map", "activity", "passes", "gallery", "captures", "scan", "stations", "satellites"} {
		if !strings.Contains(indexPage, `data-view="`+view+`"`) {
			t.Fatalf("no nav button for view %q", view)
		}
		if !strings.Contains(indexPage, `id="view-`+view+`"`) {
			t.Fatalf("no section for view %q", view)
		}
	}
	for _, asset := range []string{"/static/app.css", "/static/app.js", "/static/vendor/leaflet/leaflet.js", "/static/vendor/leaflet/leaflet.css"} {
		if !strings.Contains(indexPage, asset) {
			t.Fatalf("page does not reference %s", asset)
		}
	}
}

// TestStaticAssetsServed checks the embedded file server hands out the UI
// assets the page references.
func TestStaticAssetsServed(t *testing.T) {
	server := &Server{}
	for _, path := range []string{"/static/app.css", "/static/app.js", "/static/vendor/leaflet/leaflet.js", "/static/vendor/leaflet/leaflet.css"} {
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", path, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s: status = %d", path, recorder.Code)
		}
		if recorder.Body.Len() < 1000 {
			t.Fatalf("%s: only %d bytes", path, recorder.Body.Len())
		}
	}
}

// TestMapAPI checks the map payload stays well-formed when no TLE source is
// reachable: receivers still list, satellites degrade to a flag.
func TestMapAPI(t *testing.T) {
	server := &Server{}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/api/map.json", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	var data MapData
	if err := json.Unmarshal(recorder.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Receivers) == 0 {
		t.Fatal("catalogue receivers missing from the map payload")
	}
}

// TestTileValidation checks the tile proxy rejects out-of-range
// coordinates before any upstream request.
func TestTileValidation(t *testing.T) {
	server := &Server{}
	for _, path := range []string{"/api/tiles/20/0/0.png", "/api/tiles/2/4/0.png", "/api/tiles/-1/0/0.png", "/api/tiles/x/0/0.png"} {
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", path, nil))
		if recorder.Code == http.StatusOK {
			t.Fatalf("%s: unexpectedly served", path)
		}
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

func TestPasswordLoginProtectsUIAndAPIs(t *testing.T) {
	t.Setenv("SDRSTREAM_UI_PASSWORD", "test-demo-password")
	handler := (&Server{}).Handler()

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if recorder.Code != http.StatusSeeOther || recorder.Header().Get("Location") != "/login" {
		t.Fatalf("anonymous UI status=%d location=%q, want redirect to login", recorder.Code, recorder.Header().Get("Location"))
	}

	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/login", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `name="password"`) {
		t.Fatalf("login form missing: status=%d body=%q", recorder.Code, recorder.Body.String())
	}

	bad := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("password=wrong"))
	bad.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, bad)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password status=%d, want 401", recorder.Code)
	}

	good := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("password=test-demo-password"))
	good.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, good)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("valid password status=%d, want 303", recorder.Code)
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "sdrstream_session" || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("session cookie attributes are unsafe: %+v", cookies)
	}

	api := httptest.NewRequest(http.MethodGet, "/api/status.json", nil)
	api.AddCookie(cookies[0])
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, api)
	if recorder.Code != http.StatusOK {
		t.Errorf("authenticated API status=%d, want 200", recorder.Code)
	}

	for i := 0; i < 5; i++ {
		bad := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("password=wrong"))
		bad.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		recorder = httptest.NewRecorder()
		handler.ServeHTTP(recorder, bad)
	}
	blocked := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("password=test-demo-password"))
	blocked.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, blocked)
	if recorder.Code != http.StatusTooManyRequests {
		t.Errorf("rate-limited login status=%d, want 429", recorder.Code)
	}
}

func TestAddLiveArtifactPersistsAndServesVerifiedText(t *testing.T) {
	dir := t.TempDir()
	s := &Server{galleryDir: dir}
	activity := monitor.Activity{ID: 3, Started: time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC), FrequencyHz: 14074000, Source: "public receiver", Mode: "aprs", Kind: "data", Detail: "M0XXX>APZSAT-0:>test payload"}
	if err := s.AddLiveArtifact(activity, nil); err != nil {
		t.Fatal(err)
	}
	if len(s.liveGallery) != 1 || s.liveGallery[0].Station != activity.Source || s.liveGallery[0].FrequencyHz != int64(activity.FrequencyHz) {
		t.Fatalf("metadata not retained: %+v", s.liveGallery)
	}
	r := httptest.NewRecorder()
	s.Handler().ServeHTTP(r, httptest.NewRequest("GET", s.liveGallery[0].URL, nil))
	if r.Code != http.StatusOK || r.Body.String() != activity.Detail {
		t.Fatalf("served %d %q", r.Code, r.Body.String())
	}
}

func TestFixtureListIsLabeledAsTestOnly(t *testing.T) {
	s := &Server{}
	r := httptest.NewRecorder()
	s.Handler().ServeHTTP(r, httptest.NewRequest("GET", "/api/test-fixtures.json", nil))
	if r.Code != http.StatusOK {
		t.Fatalf("status = %d", r.Code)
	}
	if !strings.Contains(r.Body.String(), "test audio") {
		t.Fatalf("fixtures are not clearly labeled: %s", r.Body.String())
	}
}

func TestLiveGalleryServesAttachedAudio(t *testing.T) {
	dir := t.TempDir()
	audio := filepath.Join(dir, "clip.wav")
	want := []byte("RIFFtest")
	if err := os.WriteFile(audio, want, 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Server{galleryDir: filepath.Join(dir, "gallery")}
	activity := monitor.Activity{ID: 9, Started: time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC), FrequencyHz: 7415e3, Source: "public broadcast", Mode: "speech", Kind: "voice", Detail: "Today the weather will be sunny across the region with clear skies and warm temperatures. ", AudioPath: audio}
	if err := s.AddLiveArtifact(activity, nil); err != nil {
		t.Fatal(err)
	}
	if len(s.liveGallery) != 1 || s.liveGallery[0].AudioURL == "" {
		t.Fatalf("audio URL missing: %+v", s.liveGallery)
	}
	r := httptest.NewRecorder()
	s.Handler().ServeHTTP(r, httptest.NewRequest("GET", s.liveGallery[0].AudioURL, nil))
	if r.Code != http.StatusOK || !strings.HasPrefix(r.Body.String(), "RIFF") {
		t.Fatalf("audio response %d %q", r.Code, r.Body.String())
	}
}

// failedLogins drives n wrong-password logins through the handler and
// returns the last status code.
func failedLogins(handler http.Handler, password string, n int, remoteAddr, cfIP string) int {
	status := 0
	for i := 0; i < n; i++ {
		req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("password="+password))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "http://example.com")
		req.RemoteAddr = remoteAddr
		if cfIP != "" {
			req.Header.Set("CF-Connecting-IP", cfIP)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		status = recorder.Code
	}
	return status
}

func TestLoginLockoutExpiry(t *testing.T) {
	t.Setenv("SDRSTREAM_UI_PASSWORD", "test-demo-password")
	server := &Server{}
	handler := server.Handler()
	if got := failedLogins(handler, "wrong", 5, "192.0.2.10:1234", ""); got != http.StatusUnauthorized {
		t.Fatalf("fifth bad attempt status=%d, want 401 before the limit trips", got)
	}
	if got := failedLogins(handler, "wrong", 1, "192.0.2.10:1234", ""); got != http.StatusTooManyRequests {
		t.Fatalf("locked-out attempt status=%d, want 429", got)
	}
	// Age the lockout window out from under the record: the next attempt
	// must be judged on the password, not the stale counter.
	server.auth.mu.Lock()
	attempt := server.auth.attempts["192.0.2.10"]
	attempt.started = time.Now().Add(-16 * time.Minute)
	server.auth.attempts["192.0.2.10"] = attempt
	server.auth.mu.Unlock()
	if got := failedLogins(handler, "wrong", 1, "192.0.2.10:1234", ""); got != http.StatusUnauthorized {
		t.Fatalf("expired lockout status=%d, want 401 (window resets)", got)
	}
	if got := failedLogins(handler, "test-demo-password", 1, "192.0.2.10:1234", ""); got != http.StatusSeeOther {
		t.Fatalf("valid password after reset status=%d, want 303", got)
	}
}

func TestLoginRateLimitUsesCFConnectingIPBehindLoopback(t *testing.T) {
	t.Setenv("SDRSTREAM_UI_PASSWORD", "test-demo-password")
	server := &Server{}
	handler := server.Handler()
	// Behind a local cloudflared, every peer is 127.0.0.1; the per-client
	// key must come from CF-Connecting-IP or every visitor shares one budget.
	if got := failedLogins(handler, "wrong", 6, "127.0.0.1:9000", "203.0.113.9"); got != http.StatusTooManyRequests {
		t.Fatalf("sixth attempt from 203.0.113.9 status=%d, want 429", got)
	}
	if got := failedLogins(handler, "wrong", 1, "127.0.0.1:9000", "203.0.113.10"); got != http.StatusUnauthorized {
		t.Fatalf("first attempt from another client status=%d, want 401 (separate budget)", got)
	}
	// A non-loopback peer can forge CF-Connecting-IP, so the header is
	// ignored there and the limit keys on the real address.
	if got := failedLogins(handler, "wrong", 6, "198.51.100.7:9000", "203.0.113.10"); got != http.StatusTooManyRequests {
		t.Fatalf("sixth attempt from 198.51.100.7 status=%d, want 429 keyed on RemoteAddr", got)
	}
	server.auth.mu.Lock()
	headerCount := server.auth.attempts["203.0.113.10"].count
	_, keyedOnPeer := server.auth.attempts["198.51.100.7"]
	server.auth.mu.Unlock()
	if headerCount != 1 {
		t.Errorf("attempts for 203.0.113.10 = %d, want 1: the non-loopback peer must not add to a header-keyed bucket", headerCount)
	}
	if !keyedOnPeer {
		t.Error("attempts not keyed on the non-loopback peer address")
	}
}

func TestExpiredSessionCookieRedirectsAndClears(t *testing.T) {
	t.Setenv("SDRSTREAM_UI_PASSWORD", "test-demo-password")
	server := &Server{}
	handler := server.Handler()
	server.auth.mu.Lock()
	server.auth.sessions["stale-token"] = time.Now().Add(-time.Minute) // expired
	server.auth.sessions["fresh-token"] = time.Now().Add(5 * time.Minute)
	server.auth.mu.Unlock()

	stale := httptest.NewRequest(http.MethodGet, "/", nil)
	stale.AddCookie(&http.Cookie{Name: "sdrstream_session", Value: "stale-token"})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, stale)
	if recorder.Code != http.StatusSeeOther || recorder.Header().Get("Location") != "/login" {
		t.Fatalf("expired UI cookie status=%d location=%q, want redirect to /login", recorder.Code, recorder.Header().Get("Location"))
	}
	cleared := recorder.Result().Cookies()
	if len(cleared) != 1 || cleared[0].MaxAge != -1 {
		t.Fatalf("expired session not cleared: %+v", cleared)
	}
	server.auth.mu.Lock()
	_, kept := server.auth.sessions["stale-token"]
	server.auth.mu.Unlock()
	if kept {
		t.Error("expired session still in the session table")
	}

	api := httptest.NewRequest(http.MethodGet, "/api/status.json", nil)
	api.AddCookie(&http.Cookie{Name: "sdrstream_session", Value: "stale-token"})
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, api)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expired API cookie status=%d, want 401", recorder.Code)
	}
	if cleared := recorder.Result().Cookies(); len(cleared) != 1 || cleared[0].MaxAge != -1 {
		t.Fatalf("expired API cookie not cleared: %+v", cleared)
	}

	// Sliding renewal: a session inside the renewal window is extended.
	sliding := httptest.NewRequest(http.MethodGet, "/api/status.json", nil)
	sliding.AddCookie(&http.Cookie{Name: "sdrstream_session", Value: "fresh-token"})
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, sliding)
	if recorder.Code != http.StatusOK {
		t.Fatalf("valid session status=%d, want 200", recorder.Code)
	}
	renewed := recorder.Result().Cookies()
	if len(renewed) != 1 || renewed[0].Value != "fresh-token" {
		t.Fatalf("sliding renewal did not re-set the cookie: %+v", renewed)
	}
	server.auth.mu.Lock()
	expires := server.auth.sessions["fresh-token"]
	server.auth.mu.Unlock()
	if time.Until(expires) < 50*time.Minute {
		t.Errorf("session not extended: expires in %v", time.Until(expires))
	}
}

func TestLogoutRequiresPost(t *testing.T) {
	t.Setenv("SDRSTREAM_UI_PASSWORD", "test-demo-password")
	server := &Server{}
	handler := server.Handler()
	server.auth.mu.Lock()
	server.auth.sessions["bye-token"] = time.Now().Add(time.Hour)
	server.auth.mu.Unlock()

	get := httptest.NewRequest(http.MethodGet, "/logout", nil)
	get.AddCookie(&http.Cookie{Name: "sdrstream_session", Value: "bye-token"})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, get)
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /logout status=%d, want 405", recorder.Code)
	}

	post := httptest.NewRequest(http.MethodPost, "/logout", nil)
	post.AddCookie(&http.Cookie{Name: "sdrstream_session", Value: "bye-token"})
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, post)
	if recorder.Code != http.StatusSeeOther || recorder.Header().Get("Location") != "/login" {
		t.Fatalf("POST /logout status=%d, want redirect to /login", recorder.Code)
	}
	server.auth.mu.Lock()
	_, kept := server.auth.sessions["bye-token"]
	server.auth.mu.Unlock()
	if kept {
		t.Error("session survived logout")
	}
}

func TestServeRefusesPublicBindWithoutPassword(t *testing.T) {
	t.Setenv("SDRSTREAM_UI_PASSWORD", "")
	if err := (&Server{}).Serve("0.0.0.0:0"); err == nil {
		t.Fatal("Serve on a public address without a password: expected a refusal, got nil")
	}
	if err := (&Server{NoAuth: true}).Serve("not-an-address"); err == nil {
		t.Fatal("Serve with a bad address: expected an error, got nil")
	}
}

func TestScanMetricsEndpoint(t *testing.T) {
	t.Setenv("SDRSTREAM_UI_PASSWORD", "")
	s := &Server{}
	s.SetScanStatuses([]ScanStatus{{Station: "rx.example", FrequencyHz: 145500000, Mode: "fm", State: "listening"}})
	s.SetScanMetrics(ScanMetrics{RampStep: 20, MaxConnections: 100, ActiveConnections: 4, Refusals: 2, Timeouts: 1, LoadAverage: 1.2, CoreCount: 8, EstimatedBandwidthMbps: .4, BandwidthLimitMbps: 20})
	r := httptest.NewRecorder()
	s.Handler().ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/api/scans.json", nil))
	body := r.Body.String()
	for _, field := range []string{`"ramp_step": 20`, `"max_connections": 100`, `"active_connections": 4`, `"refusals": 2`, `"timeouts": 1`, `"load_average": 1.2`, `"estimated_bandwidth_mbps": 0.4`, `"stations": [`} {
		if !strings.Contains(body, field) {
			t.Errorf("scan metrics missing %s: %s", field, body)
		}
	}
}

func TestScanLogEndpointSinceAndAuth(t *testing.T) {
	t.Setenv("SDRSTREAM_UI_PASSWORD", "")
	s := &Server{}
	s.SetScanStatuses([]ScanStatus{{Station: "rx.example", StationType: "KiwiSDR", FrequencyHz: 145500000, Mode: "fm", State: "connecting", UpdatedAt: time.Unix(1, 0)}})
	s.SetScanStatuses([]ScanStatus{{Station: "rx.example", StationType: "KiwiSDR", FrequencyHz: 145500000, Mode: "fm", State: "timeout", Result: "deadline", UpdatedAt: time.Unix(2, 0)}})
	r := httptest.NewRecorder()
	s.Handler().ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/api/scanlog.json?since=1", nil))
	if !strings.Contains(r.Body.String(), `"state": "timeout"`) || !strings.Contains(r.Body.String(), `"reason": "deadline"`) {
		t.Fatalf("scan log missing event: %s", r.Body.String())
	}
}

func TestScanControlPostBoundsAndUpdates(t *testing.T) {
	t.Setenv("SDRSTREAM_UI_PASSWORD", "")
	s := &Server{scanMetrics: ScanMetrics{MaxConnections: 100, RampStep: 49}}
	bad := httptest.NewRecorder()
	badReq := httptest.NewRequest(http.MethodPost, "/api/scans/control", strings.NewReader("concurrency=101"))
	badReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	badReq.Header.Set("Origin", "http://example.com")
	s.Handler().ServeHTTP(bad, badReq)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("bad concurrency status %d", bad.Code)
	}
	ok := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/scans/control", strings.NewReader("concurrency=40&paused=true"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://example.com")
	s.Handler().ServeHTTP(ok, req)
	if ok.Code != http.StatusOK || !strings.Contains(ok.Body.String(), `"paused": true`) {
		t.Fatalf("control failed: %d %s", ok.Code, ok.Body.String())
	}
	target, paused := s.ScanControl()
	if target != 40 || !paused {
		t.Fatalf("control state=%d,%t", target, paused)
	}
}
