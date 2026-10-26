package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/common"
	"github.com/chaim12345/sdrstream/go/internal/directory"
	"github.com/chaim12345/sdrstream/go/internal/gallery"
	"github.com/chaim12345/sdrstream/go/internal/monitor"
)

// TestMain isolates the small JSON state files (auth sessions, scan state)
// into a throwaway directory so test runs never leak fixture rows into a
// live server sharing the OS temp dir - a leaked "rx.example" row is how
// fake scan entries once appeared in a real UI.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "sdrstream-web-test-state")
	if err != nil {
		panic(err)
	}
	os.Setenv("SDRSTREAM_STATE_DIR", dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

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
	// Plain HTTP (no TLS, no local tunnel) must NOT mark the cookie
	// Secure: the browser would refuse to send it and login would loop.
	if len(cookies) != 1 || cookies[0].Name != "sdrstream_session" || !cookies[0].HttpOnly || cookies[0].Secure || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("session cookie attributes are unsafe: %+v", cookies)
	}
	// M13: Secure must follow the scheme. Over plain HTTP the browser
	// drops Secure cookies, which used to make login silently impossible
	// on a http:// deployment; over https (flagged directly or by the
	// terminating proxy) the cookie must stay Secure.
	if cookies[0].Secure {
		t.Errorf("secure flag set over plain http; browsers would drop the cookie: %+v", cookies[0])
	}
	tlsGood := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("password=test-demo-password"))
	tlsGood.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	tlsGood.Header.Set("X-Forwarded-Proto", "https")
	tlsGood.RemoteAddr = "127.0.0.1:12345"
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, tlsGood)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("valid password (https) status=%d, want 303", recorder.Code)
	}
	secureCookies := recorder.Result().Cookies()
	if len(secureCookies) != 1 || !secureCookies[0].Secure {
		t.Fatalf("https session cookie must be Secure: %+v", secureCookies)
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

func TestSessionsPersistAcrossRestart(t *testing.T) {
	t.Setenv("SDRSTREAM_UI_PASSWORD", "test-demo-password")
	t.Setenv("SDRSTREAM_STATE_DIR", t.TempDir())

	first := (&Server{}).Handler()
	login := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("password=test-demo-password"))
	login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()
	first.ServeHTTP(recorder, login)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("login status=%d, want 303", recorder.Code)
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("login set %d cookies, want 1", len(cookies))
	}

	// A brand new Server (as after a process restart) must accept the
	// session the previous instance wrote to disk.
	second := (&Server{}).Handler()
	api := httptest.NewRequest(http.MethodGet, "/api/status.json", nil)
	api.AddCookie(cookies[0])
	recorder = httptest.NewRecorder()
	second.ServeHTTP(recorder, api)
	if recorder.Code != http.StatusOK {
		t.Fatalf("restarted server rejected persisted session: status=%d, want 200", recorder.Code)
	}
}

func TestSessionPersistCreatesMissingStateDir(t *testing.T) {
	t.Setenv("SDRSTREAM_UI_PASSWORD", "test-demo-password")
	t.Setenv("SDRSTREAM_STATE_DIR", filepath.Join(t.TempDir(), "nested", "state"))

	handler := (&Server{}).Handler()
	login := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("password=test-demo-password"))
	login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, login)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("login status=%d, want 303", recorder.Code)
	}
	if _, err := os.Stat(authSessionFile()); err != nil {
		t.Fatalf("session file was not written into a fresh state dir: %v", err)
	}
}

func TestExpiredSessionsAreDroppedOnLoad(t *testing.T) {
	t.Setenv("SDRSTREAM_UI_PASSWORD", "test-demo-password")
	t.Setenv("SDRSTREAM_STATE_DIR", t.TempDir())
	stale := map[string]time.Time{"dead-session": time.Now().Add(-time.Hour)}
	b, err := json.Marshal(stale)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authSessionFile(), b, 0o600); err != nil {
		t.Fatal(err)
	}

	s := &Server{}
	s.auth.init()
	if _, ok := s.auth.sessions["dead-session"]; ok {
		t.Fatalf("expired session survived reload: %v", s.auth.sessions)
	}
}

func TestActivityIncludesLiveGalleryDecodes(t *testing.T) {
	s := &Server{liveGallery: []LiveGalleryItem{{
		Station: "kiwi.test", Start: time.Now().UTC(), Mode: "speech",
		FrequencyHz: 5505000, Kind: "text", Text: "This is Shannon Volmet.",
	}}}
	r := httptest.NewRecorder()
	s.Handler().ServeHTTP(r, httptest.NewRequest("GET", "/api/activity.json", nil))
	if r.Code != http.StatusOK {
		t.Fatalf("status = %d", r.Code)
	}
	var items []monitor.Activity
	if err := json.Unmarshal(r.Body.Bytes(), &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Kind != "voice" || items[0].FrequencyHz != 5505000 || !strings.Contains(items[0].Detail, "Shannon Volmet") {
		t.Fatalf("activity = %+v, want the live gallery voice decode", items)
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

type liveTestSource struct{}

func (liveTestSource) Spectrum() ([]float64, float64, float64, bool) {
	return nil, 145e6, 2e6, false
}
func (liveTestSource) Listen(context.Context, float64, string, time.Duration) ([]float64, int, error) {
	return nil, 8000, nil
}
func (liveTestSource) Close() error { return nil }

func TestLiveEndpointWithoutMonitor(t *testing.T) {
	t.Setenv("SDRSTREAM_UI_PASSWORD", "")
	s := &Server{}
	r := httptest.NewRecorder()
	s.Handler().ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/api/live.json", nil))
	if !strings.Contains(r.Body.String(), `"available": false`) {
		t.Fatalf("expected available:false, got %s", r.Body.String())
	}
}

func TestLiveEndpointWithMonitor(t *testing.T) {
	t.Setenv("SDRSTREAM_UI_PASSWORD", "")
	s := &Server{}
	m := monitor.New(monitor.Options{Source: liveTestSource{}, Name: "test rx", Strategy: "sweep"})
	s.SetMonitor(m)
	r := httptest.NewRecorder()
	s.Handler().ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/api/live.json", nil))
	body := r.Body.String()
	for _, field := range []string{`"available": true`, `"name": "test rx"`, `"spectrum_ok": false`, `"on_air": []`} {
		if !strings.Contains(body, field) {
			t.Errorf("live snapshot missing %s: %s", field, body)
		}
	}
}

func TestScanCaptureMetadataSurvivesReload(t *testing.T) {
	dir := t.TempDir()
	if err := common.WriteWAVFile(filepath.Join(dir, "APRS_144800kHz_aprs_jukebox_spdns_eu_test.wav"), []int16{1, 2, 3}, 8000); err != nil {
		t.Fatal(err)
	}
	s := &Server{capturesDir: dir}
	s.AddCaptureMetadata("APRS_144800kHz_aprs_jukebox_spdns_eu_test.wav", "aprs", "jukebox.spdns.eu:6166", "4X1RF>APRS:!abc", 144800000, true)
	got := s.scanCaptures()
	if len(got) != 1 || got[0].FrequencyHz != 144800000 || got[0].Mode != "aprs" || got[0].Receiver != "jukebox.spdns.eu:6166" || got[0].Decode != "4X1RF>APRS:!abc" {
		t.Fatalf("capture metadata = %+v", got)
	}
}

func TestScanCaptureReadsRecordAllMetadata(t *testing.T) {
	dir := t.TempDir()
	name := "DATA_7074kHz_ft8_kiwi_test_8073_20261022T092000Z_1.wav"
	if err := common.WriteWAVFile(filepath.Join(dir, name), []int16{1, 2, 3}, 8000); err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 10, 22, 9, 20, 0, 0, time.UTC)
	if err := WriteCaptureMetadata(dir, name, CaptureMetadata{
		FrequencyHz: 7074000, Mode: "ft8", Receiver: "kiwi.test:8073",
		Detail: "CQ DL1ABC", Kind: "data", Target: "FT8 40m",
		Gate: "2 messages", Started: started, DurationSec: 15, Verified: true,
	}); err != nil {
		t.Fatal(err)
	}
	s := &Server{capturesDir: dir}
	got := s.scanCaptures()
	if len(got) != 1 {
		t.Fatalf("captures = %+v, want one", got)
	}
	capture := got[0]
	if capture.Kind != "data" || capture.Target != "FT8 40m" || !capture.Verified || capture.Gate != "2 messages" {
		t.Fatalf("record-all metadata = %+v", capture)
	}
	if !capture.Start.Equal(started) {
		t.Fatalf("start = %v, want the sidecar timestamp %v", capture.Start, started)
	}
}

// TestMonitorControl exercises the live receiver knobs: method and origin
// checks, validation, no-monitor 404, and that accepted values show up in
// the monitor's live snapshot.
func TestMonitorControl(t *testing.T) {
	post := func(server *Server, origin, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "http://example.test/api/monitor/control", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", origin)
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, req)
		return rec
	}

	noMonitor := &Server{}
	if rec := post(noMonitor, "http://example.test", "threshold_db=12"); rec.Code != http.StatusNotFound {
		t.Fatalf("no monitor: status = %d, want 404", rec.Code)
	}

	server := &Server{}
	instance := monitor.New(monitor.Options{ThresholdDB: 8, StepHz: 25000, Mode: "auto"})
	server.SetMonitor(instance)

	req := httptest.NewRequest("GET", "/api/monitor/control", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: status = %d, want 405", rec.Code)
	}
	if rec := post(server, "http://evil.test", "threshold_db=12"); rec.Code != http.StatusForbidden {
		t.Fatalf("cross-origin: status = %d, want 403", rec.Code)
	}
	for _, body := range []string{"threshold_db=0", "threshold_db=99", "step_hz=7777", "mode=ssb"} {
		if rec := post(server, "http://example.test", body); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400", body, rec.Code)
		}
	}
	rec = post(server, "http://example.test", "threshold_db=14&step_hz=12500&mode=nfm")
	if rec.Code != http.StatusOK {
		t.Fatalf("valid: status = %d, body %s", rec.Code, rec.Body.String())
	}
	info := instance.Live()
	if info.ThresholdDB != 14 || info.StepHz != 12500 || info.Mode != "nfm" {
		t.Fatalf("live snapshot = %+v", info)
	}
	// A partial update keeps the other knobs.
	if rec := post(server, "http://example.test", "threshold_db=6"); rec.Code != http.StatusOK {
		t.Fatalf("partial: status = %d", rec.Code)
	}
	info = instance.Live()
	if info.ThresholdDB != 6 || info.StepHz != 12500 || info.Mode != "nfm" {
		t.Fatalf("after partial update = %+v", info)
	}
}

func TestTuneEndpoint(t *testing.T) {
	t.Setenv("SDRSTREAM_UI_PASSWORD", "")
	s := &Server{}
	// No monitor: tuning is not possible.
	r := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/tune.json", strings.NewReader("frequency_hz=144400000"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://example.com")
	s.Handler().ServeHTTP(r, req)
	if r.Code != http.StatusNotFound {
		t.Fatalf("no monitor: status %d", r.Code)
	}
	// With a monitor the tune is accepted and echoed.
	s.SetMonitor(monitor.New(monitor.Options{Source: liveTestSource{}, Name: "test rx"}))
	bad := httptest.NewRecorder()
	s.Handler().ServeHTTP(bad, req)
	if bad.Code != http.StatusOK {
		t.Fatalf("tune with monitor: status %d", bad.Code)
	}
	if !strings.Contains(bad.Body.String(), `"tuned_hz": 144400000`) {
		t.Fatalf("tune response: %s", bad.Body.String())
	}
	// Cross-origin posts are refused.
	xr := httptest.NewRecorder()
	xreq := httptest.NewRequest(http.MethodPost, "/api/tune.json", strings.NewReader("frequency_hz=144400000"))
	xreq.Header.Set("Origin", "https://evil.example")
	s.Handler().ServeHTTP(xr, xreq)
	if xr.Code != http.StatusForbidden {
		t.Fatalf("cross-origin tune: status %d", xr.Code)
	}
}

func TestAddLiveArtifactFeedsRdioOnlyWhenAudioExists(t *testing.T) {
	dir := t.TempDir()
	s := &Server{galleryDir: dir}
	audio := filepath.Join(t.TempDir(), "capture.wav")
	if err := os.WriteFile(audio, []byte("RIFF....WAVE"), 0o644); err != nil {
		t.Fatal(err)
	}
	var hooked []monitor.Activity
	s.SetRdioHook(func(activity monitor.Activity) { hooked = append(hooked, activity) })

	withAudio := monitor.Activity{ID: 7, Started: time.Now().UTC(), FrequencyHz: 119800000, Source: "receiver", Mode: "speech", Kind: "voice", Detail: "tower this is november eight seven two zero cleared to land runway two seven wind two six zero at eight", AudioPath: audio}
	if err := s.AddLiveArtifact(withAudio, nil); err != nil {
		t.Fatal(err)
	}
	if len(hooked) != 1 || hooked[0].AudioPath != audio {
		t.Fatalf("rdio hook = %+v, want one call with audio", hooked)
	}

	noAudio := monitor.Activity{ID: 8, Started: time.Now().UTC(), FrequencyHz: 14074000, Source: "receiver", Mode: "aprs", Kind: "data", Detail: "M0XXX>APZSAT-0:>test payload"}
	if err := s.AddLiveArtifact(noAudio, nil); err != nil {
		t.Fatal(err)
	}
	if len(hooked) != 1 {
		t.Fatalf("rdio hook called without audio: %+v", hooked)
	}
}

// The lockout map must not grow forever: an attacker rotating source
// addresses leaks one entry per attempt otherwise. The eviction sweep
// (expired first, then oldest-shed) keeps it bounded.
func TestLoginAttemptsMapCapped(t *testing.T) {
	previous := maxLoginAttemptsTracked
	maxLoginAttemptsTracked = 100
	t.Cleanup(func() { maxLoginAttemptsTracked = previous })
	t.Setenv("SDRSTREAM_UI_PASSWORD", "test-demo-password")

	s := &Server{}
	_ = s.Handler()
	playback := func() {
		for ip := 0; ip < 250; ip++ {
			req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(fmt.Sprintf("password=wrong-ip-%03d", ip)))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.RemoteAddr = fmt.Sprintf("10.%d.%d.%d:55555", ip/256, ip%256, 1)
			recorder := httptest.NewRecorder()
			s.Handler().ServeHTTP(recorder, req)
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("attempt %d status=%d, want 401", ip, recorder.Code)
			}
		}
	}
	playback()
	s.auth.mu.Lock()
	size := len(s.auth.attempts)
	s.auth.mu.Unlock()
	// The length can touch the cap (eviction fires on the next login at
	// a full map) but must never exceed it.
	if size > 100 {
		t.Fatalf("lockout map grew to %d entries with a cap of 100", size)
	}
}

// TestPWARoutes pins the installable-app surface: the manifest and service
// worker are served at root scope with usable content types, the manifest
// parses and names icon URLs that actually exist in the embedded static FS,
// and the page links the manifest.
func TestPWARoutes(t *testing.T) {
	srv := (&Server{}).Handler()

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/manifest.webmanifest", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("manifest status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/manifest+json" {
		t.Fatalf("manifest content type = %q", ct)
	}
	var manifest struct {
		Name       string `json:"name"`
		StartURL   string `json:"start_url"`
		Display    string `json:"display"`
		ThemeColor string `json:"theme_color"`
		Icons      []struct {
			Src   string `json:"src"`
			Sizes string `json:"sizes"`
		} `json:"icons"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &manifest); err != nil {
		t.Fatalf("manifest does not parse: %v", err)
	}
	if manifest.Name == "" || manifest.StartURL != "/" || manifest.Display != "standalone" {
		t.Fatalf("manifest missing installable fields: %+v", manifest)
	}
	if len(manifest.Icons) < 2 {
		t.Fatalf("manifest has %d icons, need at least 192 and 512", len(manifest.Icons))
	}
	has192, has512 := false, false
	for _, icon := range manifest.Icons {
		if !strings.HasPrefix(icon.Src, "/static/") {
			t.Fatalf("icon %q is not under /static/", icon.Src)
		}
		if _, err := staticFS.ReadFile(strings.TrimPrefix(icon.Src, "/")); err != nil {
			t.Fatalf("manifest icon %q not in embedded FS: %v", icon.Src, err)
		}
		has192 = has192 || strings.Contains(icon.Sizes, "192x192")
		has512 = has512 || strings.Contains(icon.Sizes, "512x512")
	}
	if !has192 || !has512 {
		t.Fatalf("manifest lacks 192x192 and 512x512 icons")
	}

	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/sw.js", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("sw.js status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/javascript") {
		t.Fatalf("sw.js content type = %q", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("sw.js must be no-cache so updates propagate, got %q", cc)
	}
	if !strings.Contains(rec.Body.String(), "addEventListener('fetch'") {
		t.Fatal("sw.js does not look like a service worker")
	}

	if !strings.Contains(indexPage, `rel="manifest" href="/manifest.webmanifest"`) {
		t.Fatal("index.html does not link the web app manifest")
	}
	if !strings.Contains(indexPage, "apple-touch-icon") {
		t.Fatal("index.html lost the apple-touch-icon link")
	}

	// The CSP must allow the manifest and the service worker from self.
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	csp := rec.Header().Get("Content-Security-Policy")
	for _, directive := range []string{"manifest-src 'self'", "worker-src 'self'"} {
		if !strings.Contains(csp, directive) {
			t.Fatalf("CSP missing %q: %s", directive, csp)
		}
	}
}

func TestMonitorLifecycleControl(t *testing.T) {
	post := func(server *Server, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "http://example.test/api/monitor/control", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "http://example.test")
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, req)
		return rec
	}

	// Without hooks the lifecycle actions report unsupported.
	noHooks := &Server{}
	if rec := post(noHooks, "action=start&server=rx.example:8073&kind=openwebrx"); rec.Code != http.StatusNotImplemented {
		t.Fatalf("start without hooks: status = %d, want 501", rec.Code)
	}
	if rec := post(noHooks, "action=stop"); rec.Code != http.StatusNotImplemented {
		t.Fatalf("stop without hooks: status = %d, want 501", rec.Code)
	}

	// With hooks, start validates input and forwards a normalized spec.
	var got MonitorStartSpec
	stopped := 0
	server := &Server{}
	server.SetMonitorHooks(MonitorHooks{
		Start: func(spec MonitorStartSpec) error { got = spec; return nil },
		Stop:  func() error { stopped++; return nil },
	})
	for _, body := range []string{
		"action=start&server=&kind=openwebrx",
		"action=start&server=rx.example:8073&kind=morse",
		"action=start&server=rx%20bad:8073&kind=openwebrx",
		"action=start&server=rx.example%40evil:8073&kind=openwebrx",
		"action=start&server=ab&kind=openwebrx",
		"action=start&server=rx.example:8073&kind=openwebrx&profile=bad%20profile",
	} {
		if rec := post(server, body); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400", body, rec.Code)
		}
	}
	rec := post(server, "action=start&server=http%3A%2F%2Fjukebox.spdns.eu%3A8073%2F&kind=openwebrx&profile=0m&label=Jukebox")
	if rec.Code != http.StatusOK {
		t.Fatalf("valid start: status = %d, body %s", rec.Code, rec.Body.String())
	}
	if got.Server != "jukebox.spdns.eu:8073" || got.Kind != "openwebrx" || got.Profile != "0m" || got.Label != "Jukebox" {
		t.Fatalf("spec = %+v", got)
	}
	if rec := post(server, "action=start&server=websdr.ewi.utwente.nl%3A8901&kind=WebSDR"); rec.Code != http.StatusOK {
		t.Fatalf("kind case-insensitive: status = %d", rec.Code)
	}
	if got.Kind != "websdr" {
		t.Fatalf("kind = %q, want websdr", got.Kind)
	}
	if rec := post(server, "action=stop"); rec.Code != http.StatusOK || stopped != 1 {
		t.Fatalf("stop: status = %d, stopped = %d", rec.Code, stopped)
	}
	// Stop is idempotent.
	if rec := post(server, "action=stop"); rec.Code != http.StatusOK || stopped != 2 {
		t.Fatalf("idempotent stop: status = %d, stopped = %d", rec.Code, stopped)
	}

	// A hook failure surfaces as 502 so the UI can show a failed state.
	broken := &Server{}
	broken.SetMonitorHooks(MonitorHooks{Start: func(spec MonitorStartSpec) error { return errors.New("dial refused") }})
	if rec := post(broken, "action=start&server=rx.example:8073&kind=openwebrx"); rec.Code != http.StatusBadGateway {
		t.Fatalf("hook failure: status = %d, want 502", rec.Code)
	}
}

func TestLoginFailureRendersFormWithInlineError(t *testing.T) {
	t.Setenv("SDRSTREAM_UI_PASSWORD", "test-demo-password")
	handler := (&Server{}).Handler()

	post := func(password string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("password="+password))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	// A wrong password must re-render the form with an inline error,
	// not strand the user on a bare text page (QA finding).
	rec := post("wrong")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password status=%d, want 401", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `name="password"`) || !strings.Contains(body, "Invalid password") {
		t.Fatalf("401 page must contain the form and the inline error, got: %q", body)
	}

	// After 5 failed attempts the lockout page keeps the form too and
	// carries Retry-After.
	for i := 0; i < 4; i++ {
		post("wrong")
	}
	rec = post("wrong")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("6th attempt status=%d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") != "900" {
		t.Fatalf("Retry-After=%q, want 900", rec.Header().Get("Retry-After"))
	}
	body = rec.Body.String()
	if !strings.Contains(body, `name="password"`) || !strings.Contains(body, "Too many attempts") {
		t.Fatalf("429 page must contain the form and the lockout message, got: %q", body)
	}
}

func TestLiveGalleryPersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("live-20261023T070000Z-1.txt", "CQ TEST JO02")
	write("live-20261023T070000Z-1.txt.wav", "RIFF-fake-audio-bytes")
	write("live-20261023T070100Z-2.txt", "no audio clip")
	// An orphan file with no index entry must NOT become reachable.
	write("live-orphan.txt", "not indexed")

	first := &Server{galleryDir: dir}
	first.liveGallery = []LiveGalleryItem{
		{Name: "live-20261023T070000Z-1.txt", URL: "/gallery/live-20261023T070000Z-1.txt", AudioURL: "/audio/live/live-20261023T070000Z-1.txt.wav", Text: "CQ TEST JO02"},
		{Name: "live-20261023T070100Z-2.txt", URL: "/gallery/live-20261023T070100Z-2.txt", AudioURL: "/audio/live/live-20261023T070100Z-2.txt.wav", Text: "no audio clip"},
	}
	if err := first.persistLiveGalleryLocked(); err != nil {
		t.Fatal(err)
	}

	// Simulate an engine restart: a fresh Server over the same dir.
	restarted := &Server{galleryDir: dir}
	restarted.loadLiveGallery()
	if len(restarted.liveGallery) != 2 {
		t.Fatalf("reloaded %d items, want 2", len(restarted.liveGallery))
	}
	// The clip with its WAV keeps audio_url.
	if restarted.liveGallery[0].AudioURL == "" {
		t.Fatalf("audio_url lost for item with existing WAV: %+v", restarted.liveGallery[0])
	}
	// The clip whose WAV is missing keeps the text but loses audio_url -
	// the UI must show "audio unavailable", not a player that 404s.
	if restarted.liveGallery[1].AudioURL != "" {
		t.Fatalf("audio_url kept for missing WAV: %+v", restarted.liveGallery[1])
	}
	// The routes now serve the restored items.
	req := httptest.NewRequest(http.MethodGet, "/audio/live/live-20261023T070000Z-1.txt.wav", nil)
	rec := httptest.NewRecorder()
	restarted.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "RIFF-fake-audio-bytes" {
		t.Fatalf("restored audio: status=%d body=%q", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/gallery/live-20261023T070100Z-2.txt", nil)
	rec = httptest.NewRecorder()
	restarted.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("restored artifact: status=%d", rec.Code)
	}
	// The orphan stays unreachable.
	req = httptest.NewRequest(http.MethodGet, "/gallery/live-orphan.txt", nil)
	rec = httptest.NewRecorder()
	restarted.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("orphan file: status=%d, want 404", rec.Code)
	}
}

func TestLogTailEndpoint(t *testing.T) {
	ring := NewLogRing(3)
	_, _ = ring.Write([]byte("line one\nline two\n"))
	_, _ = ring.Write([]byte("line three\nline four\n"))
	server := &Server{}
	server.SetLogRing(ring)
	req := httptest.NewRequest(http.MethodGet, "/api/logs/tail?lines=2", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	var out struct {
		Lines []string `json:"lines"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Lines) != 2 || out.Lines[0] != "line three" || out.Lines[1] != "line four" {
		t.Fatalf("tail = %v", out.Lines)
	}
	// Ring cap honoured: line one was evicted.
	req = httptest.NewRequest(http.MethodGet, "/api/logs/tail?lines=500", nil)
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	out.Lines = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Lines) != 3 || out.Lines[0] != "line two" {
		t.Fatalf("capped tail = %v", out.Lines)
	}
	// Out-of-range lines param rejected.
	req = httptest.NewRequest(http.MethodGet, "/api/logs/tail?lines=9999", nil)
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("lines=9999: status=%d, want 400", rec.Code)
	}
	// No ring configured: honest 404.
	bare := &Server{}
	req = httptest.NewRequest(http.MethodGet, "/api/logs/tail", nil)
	rec = httptest.NewRecorder()
	bare.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unconfigured: status=%d, want 404", rec.Code)
	}
}

func TestGalleryManifestLoadAndSourceFields(t *testing.T) {
	dir := t.TempDir()
	png := append([]byte("\x89PNG\r\n\x1a\n"), []byte("pixels")...)
	for _, name := range []string{"42-a.png", "7-legacy.png"} {
		if err := os.WriteFile(filepath.Join(dir, name), png, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// One item with full provenance, one legacy item without the source field.
	items := []gallery.Item{
		{Source: "satnogs", ObservationURL: gallery.ObservationLink(42), ObservationID: 42,
			Satellite: "ISS", Station: "Veve-UHF", Kind: "image", Name: "42-a.png",
			Start: time.Unix(1_700_000_000, 0), Path: filepath.Join(dir, "42-a.png")},
		{ObservationID: 7, Satellite: "ISS", Station: "old", Kind: "image",
			Name: "7-legacy.png", Start: time.Unix(1_699_000_000, 0), Path: filepath.Join(dir, "7-legacy.png")},
	}
	if err := gallery.WriteManifest(items, dir); err != nil {
		t.Fatal(err)
	}
	server := &Server{galleryDir: dir, scanIndex: make(map[string]int)}
	server.loadGallery()
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/api/gallery.json", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	var out []gallery.Item
	if err := json.Unmarshal(recorder.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("got %d items, want 2", len(out))
	}
	byID := map[int]gallery.Item{}
	for _, it := range out {
		byID[it.ObservationID] = it
	}
	full := byID[42]
	if full.Source != "satnogs" || full.ObservationURL != "https://network.satnogs.org/observations/42/" {
		t.Errorf("provenance lost through manifest load: %+v", full)
	}
	if legacy := byID[7]; legacy.Source != "satnogs" {
		t.Errorf("legacy manifest item source = %q, want backfilled satnogs", legacy.Source)
	}
}

// Archived audio artefacts must be playable in the browser: the manifest
// name serves the recording with an audio MIME type and byte-range
// support, so the gallery's <audio> element can stream and seek it.
func TestGalleryFileServesPlayableAudio(t *testing.T) {
	server := newTestServer(t)
	wav := append([]byte("RIFF\x24\x00\x00\x00WAVEfmt "), []byte("fake-wav-body")...)
	name := "7-pass.wav"
	if err := os.WriteFile(filepath.Join(server.galleryDir, name), wav, 0o644); err != nil {
		t.Fatal(err)
	}
	server.gallery = append(server.gallery, gallery.Item{
		ObservationID: 7, Satellite: "ISS", Station: "test", Kind: "audio",
		Name: name, Start: time.Unix(1_700_000_000, 0),
		Path: filepath.Join(server.galleryDir, name),
	})
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/gallery/"+name, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if ct := recorder.Header().Get("Content-Type"); ct != "audio/wav" {
		t.Errorf("Content-Type = %q, want audio/wav", ct)
	}
	if ar := recorder.Header().Get("Accept-Ranges"); ar != "bytes" {
		t.Errorf("Accept-Ranges = %q, want bytes", ar)
	}
	recorder = httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/gallery/"+name, nil)
	req.Header.Set("Range", "bytes=0-3")
	server.Handler().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusPartialContent || recorder.Body.String() != "RIFF" {
		t.Errorf("range request: status = %d, body = %q; want 206 RIFF", recorder.Code, recorder.Body.String())
	}
}

// The shell (document, static assets, service worker) must revalidate on
// every load: without Cache-Control: no-cache, an installed browser can
// keep running the previous frontend after an app upgrade because the
// responses carry no validator at all.
func TestShellAssetsRevalidate(t *testing.T) {
	server := newTestServer(t)
	for _, path := range []string{"/", "/static/app.css", "/static/app.js", "/sw.js", "/manifest.webmanifest"} {
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", path, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s: status = %d", path, recorder.Code)
		}
		if cc := recorder.Header().Get("Cache-Control"); cc != "no-cache" {
			t.Errorf("%s: Cache-Control = %q, want no-cache", path, cc)
		}
	}
}

// TestShellWiresMsglog pins the integration wiring of the decoder-history
// enhancement: the page must load msglog.js AFTER app.js (it extends the
// globals app.js defines), and the service worker shell must carry the new
// asset under a bumped cache name so old shells are dropped.
func TestShellWiresMsglog(t *testing.T) {
	page, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	appIdx := strings.Index(string(page), `<script src="/static/app.js"></script>`)
	msgIdx := strings.Index(string(page), `<script src="/static/msglog.js"></script>`)
	if appIdx < 0 || msgIdx < 0 {
		t.Fatalf("index.html must load app.js and msglog.js (app at %d, msglog at %d)", appIdx, msgIdx)
	}
	if msgIdx < appIdx {
		t.Fatal("msglog.js must load after app.js")
	}
	sw, err := staticFS.ReadFile("static/sw.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sw), "'/static/msglog.js'") {
		t.Fatal("service worker shell missing /static/msglog.js")
	}
	if !strings.Contains(string(sw), "const CACHE = 'sdrstream-shell-v5';") {
		t.Fatal("service worker CACHE not bumped for the msglog shell change")
	}
	if _, err := staticFS.ReadFile("static/msglog.js"); err != nil {
		t.Fatal("static/msglog.js not embedded")
	}
}

// The message log has exactly one renderer: msglog.js owns it, and app.js
// must not keep a legacy copy. A second renderer ran beside it (the 4 s
// poll interval captured the legacy binding before msglog.js replaced it)
// and the two wrote conflicting chips, state lines and item sets in the
// live browser while the fake-DOM suites stayed green.
func TestAppJsHasNoLegacyMsglogRenderer(t *testing.T) {
	data, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	appjs := string(data)
	for _, marker := range []string{"scroll paused", "slice(-300)", "bar.dataset.key"} {
		if strings.Contains(appjs, marker) {
			t.Fatalf("app.js still carries the legacy msglog renderer marker %q", marker)
		}
	}
	// The replaceable bindings must stay declared: msglog.js is a
	// strict-mode IIFE that assigns through them, and the 4 s poll must
	// look the renderer up per tick, never capture the binding.
	for _, marker := range []string{"function renderMsglog() {}", "function msglogText() {", "setInterval(() => renderMsglog(), 4000)"} {
		if !strings.Contains(appjs, marker) {
			t.Fatalf("app.js is missing the msglog wiring marker %q", marker)
		}
	}
}

// TestSpyServersFailureIsNotEmptiness mirrors the stations contract: a
// directory outage is a 502, not a 200 with an empty list.
func TestSpyServersFailureIsNotEmptiness(t *testing.T) {
	server := &Server{fetchSpyServers: func(context.Context) ([]directory.SpyServer, error) {
		return nil, errors.New("directory offline")
	}}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/api/spyservers.json", nil))
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", recorder.Code)
	}
}

// TestSpyServersServesStaleCache serves the expired list on refresh
// failure, marked stale.
func TestSpyServersServesStaleCache(t *testing.T) {
	server := &Server{
		spyServers:   []directory.SpyServer{{Host: "8.8.8.8", Port: 5555, DeviceType: "AirspyOne", Online: true}},
		spyServersAt: time.Now().Add(-2 * time.Hour),
		fetchSpyServers: func(context.Context) ([]directory.SpyServer, error) {
			return nil, errors.New("directory offline")
		},
	}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/api/spyservers.json", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want the cached list", recorder.Code)
	}
	if recorder.Header().Get("X-Stations-Stale") != "true" {
		t.Fatal("stale cache not marked")
	}
	if !strings.Contains(recorder.Body.String(), "8.8.8.8") {
		t.Fatalf("cached entry missing: %s", recorder.Body.String())
	}
}

// TestSpyServersEmptyDirectory: an answering-but-empty directory is a
// normal empty list.
func TestSpyServersEmptyDirectory(t *testing.T) {
	server := &Server{fetchSpyServers: func(context.Context) ([]directory.SpyServer, error) {
		return []directory.SpyServer{}, nil
	}}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/api/spyservers.json", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if got := strings.TrimSpace(recorder.Body.String()); got != "[]" {
		t.Errorf("body = %q, want an empty list", got)
	}
}

// A shared preview build refuses monitor start with an explicit reason so
// a visitor cannot make the demo engine dial a real receiver.
func TestMonitorStartPreviewGate(t *testing.T) {
	t.Setenv("SDRSTREAM_PREVIEW_NO_START", "1")
	s := &Server{}
	req := httptest.NewRequest("POST", "http://example.test/api/monitor/start", strings.NewReader("kind=openwebrx&server=x"))
	rec := httptest.NewRecorder()
	s.handleMonitorStart(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("preview gate: got %d, want 403", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "preview demo") {
		t.Fatalf("preview gate: body %q should name the reason", rec.Body.String())
	}
}

// Without the preview env the gate stays out of the way (the existing
// not-supported path proves the request reached the normal handler).
func TestMonitorStartPreviewGateOff(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest("POST", "http://example.test/api/monitor/start", strings.NewReader("kind=openwebrx&server=x"))
	rec := httptest.NewRecorder()
	s.handleMonitorStart(rec, req)
	if rec.Code == http.StatusForbidden {
		t.Fatalf("gate off: got 403, want the normal handler path")
	}
}

// The status payload tells the UI when this build refuses monitor start so
// empty states do not invite an action the preview will refuse.
func TestStatusPreviewNoStart(t *testing.T) {
	t.Setenv("SDRSTREAM_PREVIEW_NO_START", "1")
	s := &Server{}
	rec := httptest.NewRecorder()
	s.handleStatus(rec, httptest.NewRequest("GET", "/api/status.json", nil))
	var on map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &on); err != nil {
		t.Fatal(err)
	}
	if on["preview_no_start"] != true {
		t.Fatalf("status with preview gate: %s", rec.Body.String())
	}
	t.Setenv("SDRSTREAM_PREVIEW_NO_START", "")
	rec2 := httptest.NewRecorder()
	s.handleStatus(rec2, httptest.NewRequest("GET", "/api/status.json", nil))
	var off map[string]interface{}
	if err := json.Unmarshal(rec2.Body.Bytes(), &off); err != nil {
		t.Fatal(err)
	}
	if _, present := off["preview_no_start"]; present {
		t.Fatalf("status without preview gate must omit the flag: %s", rec2.Body.String())
	}
}
