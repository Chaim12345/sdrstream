package web

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/common"
	"github.com/chaim12345/sdrstream/go/internal/monitor"
	"github.com/chaim12345/sdrstream/go/internal/satnogs"
)

// TestServerTimeouts pins the http.Server hardening: no timeout field may
// be left at its zero (unbounded) value.
func TestServerTimeouts(t *testing.T) {
	server := (&Server{}).newHTTPServer("127.0.0.1:0")
	if server.ReadHeaderTimeout != 10*time.Second {
		t.Errorf("ReadHeaderTimeout = %v, want 10s", server.ReadHeaderTimeout)
	}
	if server.ReadTimeout != 30*time.Second {
		t.Errorf("ReadTimeout = %v, want 30s", server.ReadTimeout)
	}
	// WriteTimeout must be long enough to stream a large WAV over a slow
	// receiver link; 10 minutes is the chosen allowance.
	if server.WriteTimeout != 10*time.Minute {
		t.Errorf("WriteTimeout = %v, want 10m (audio streaming)", server.WriteTimeout)
	}
	if server.IdleTimeout != 120*time.Second {
		t.Errorf("IdleTimeout = %v, want 120s", server.IdleTimeout)
	}
}

// TestSessionsInvalidatedOnPasswordChange: a session minted under one UI
// password must stop working after the password changes (and the server
// restarts, which is how the new password takes effect).
func TestSessionsInvalidatedOnPasswordChange(t *testing.T) {
	t.Setenv("SDRSTREAM_STATE_DIR", t.TempDir())
	t.Setenv("SDRSTREAM_UI_PASSWORD", "password-one")
	first := (&Server{}).Handler()
	login := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("password=password-one"))
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

	// Same state dir (a restart), new password: the persisted session must
	// be refused even though it has not expired.
	t.Setenv("SDRSTREAM_UI_PASSWORD", "password-two")
	second := (&Server{}).Handler()
	api := httptest.NewRequest(http.MethodGet, "/api/status.json", nil)
	api.AddCookie(cookies[0])
	recorder = httptest.NewRecorder()
	second.ServeHTTP(recorder, api)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("session from before the password change: status=%d, want 401", recorder.Code)
	}

	// Control: unchanged password keeps the session valid.
	t.Setenv("SDRSTREAM_UI_PASSWORD", "password-one")
	third := (&Server{}).Handler()
	recorder = httptest.NewRecorder()
	third.ServeHTTP(recorder, api)
	if recorder.Code != http.StatusOK {
		t.Fatalf("same-password restart rejected the session: status=%d, want 200", recorder.Code)
	}
}

// TestSessionCookieSecureFlag: Secure is set only when the request arrived
// over HTTPS directly or via a local tunnel, never on plain HTTP (where a
// Secure cookie would break login) and never on a forwarded proto header
// from a non-loopback peer (attacker-controlled).
func TestSessionCookieSecureFlag(t *testing.T) {
	t.Setenv("SDRSTREAM_STATE_DIR", t.TempDir())
	t.Setenv("SDRSTREAM_UI_PASSWORD", "test-demo-password")
	handler := (&Server{}).Handler()
	login := func() *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("password=test-demo-password"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return req
	}
	cookieSecure := func(req *http.Request) bool {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		cookies := recorder.Result().Cookies()
		if len(cookies) != 1 {
			t.Fatalf("login set %d cookies, want 1", len(cookies))
		}
		return cookies[0].Secure
	}

	if cookieSecure(login()) {
		t.Error("plain HTTP from a routable peer got a Secure cookie")
	}
	tlsReq := login()
	tlsReq.TLS = &tls.ConnectionState{}
	if !cookieSecure(tlsReq) {
		t.Error("direct TLS did not get a Secure cookie")
	}
	tunnel := login()
	tunnel.RemoteAddr = "127.0.0.1:41000"
	tunnel.Header.Set("X-Forwarded-Proto", "https")
	if !cookieSecure(tunnel) {
		t.Error("local tunnel (loopback + X-Forwarded-Proto https) did not get a Secure cookie")
	}
	forged := login()
	forged.RemoteAddr = "203.0.113.5:41000"
	forged.Header.Set("X-Forwarded-Proto", "https")
	if cookieSecure(forged) {
		t.Error("X-Forwarded-Proto from a non-loopback peer was trusted")
	}
	plainLoopback := login()
	plainLoopback.RemoteAddr = "127.0.0.1:41000"
	if cookieSecure(plainLoopback) {
		t.Error("plain HTTP on loopback got a Secure cookie the browser would refuse")
	}
}

// TestLoginRejectsForeignOrigin pins the CSRF hardening on the login form:
// a cross-origin POST is refused before the password is even checked.
func TestLoginRejectsForeignOrigin(t *testing.T) {
	t.Setenv("SDRSTREAM_STATE_DIR", t.TempDir())
	t.Setenv("SDRSTREAM_UI_PASSWORD", "test-demo-password")
	handler := (&Server{}).Handler()
	post := func(origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("password=test-demo-password"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		return recorder
	}
	if r := post("https://evil.example"); r.Code != http.StatusForbidden {
		t.Fatalf("cross-origin login status=%d, want 403", r.Code)
	}
	if r := post("http://example.com"); r.Code != http.StatusSeeOther {
		t.Fatalf("same-origin login status=%d, want 303", r.Code)
	}
	if r := post(""); r.Code != http.StatusSeeOther {
		t.Fatalf("origin-less login (curl) status=%d, want 303", r.Code)
	}
}

// TestTuneRejectsUnknownMode: the tune endpoint whitelists listening modes
// instead of passing any string to the receiver.
func TestTuneRejectsUnknownMode(t *testing.T) {
	t.Setenv("SDRSTREAM_UI_PASSWORD", "")
	s := &Server{}
	s.SetMonitor(monitor.New(monitor.Options{Source: liveTestSource{}, Name: "test rx"}))
	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/tune.json", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "http://example.com")
		recorder := httptest.NewRecorder()
		s.Handler().ServeHTTP(recorder, req)
		return recorder
	}
	if r := post("frequency_hz=144400000&mode=rm -rf /"); r.Code != http.StatusBadRequest {
		t.Fatalf("injected mode status=%d, want 400", r.Code)
	}
	if r := post("frequency_hz=144400000&mode=ssb"); r.Code != http.StatusBadRequest {
		t.Fatalf("unknown mode ssb status=%d, want 400", r.Code)
	}
	if r := post("frequency_hz=144400000&mode=usb"); r.Code != http.StatusOK {
		t.Fatalf("whitelisted mode usb status=%d, want 200", r.Code)
	}
	if r := post("frequency_hz=144400000"); r.Code != http.StatusOK {
		t.Fatalf("default (empty) mode status=%d, want 200", r.Code)
	}
}

// TestLiveAudioRequiresExactName: a suffix look-alike must not serve
// another item's audio.
func TestLiveAudioRequiresExactName(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"live-20261021T120000Z-1.txt.wav", "clip.wav"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("RIFFtest"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{galleryDir: dir, liveGallery: []LiveGalleryItem{
		{Name: "live-20261021T120000Z-1.txt", AudioURL: "/audio/live/live-20261021T120000Z-1.txt.wav"},
	}}
	for _, path := range []string{"/audio/live/1.txt.wav", "/audio/live/clip.wav", "/audio/live/T120000Z-1.txt.wav"} {
		recorder := httptest.NewRecorder()
		s.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", path, nil))
		if recorder.Code != http.StatusNotFound {
			t.Errorf("%s: status=%d, want 404 (suffix match must not serve)", path, recorder.Code)
		}
	}
	recorder := httptest.NewRecorder()
	s.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/audio/live/live-20261021T120000Z-1.txt.wav", nil))
	if recorder.Code != http.StatusOK {
		t.Errorf("exact name: status=%d, want 200", recorder.Code)
	}
}

// TestCSPDropsUnsafeInlineScripts pins both the header and the page
// invariant that justifies it: no inline <script> blocks exist.
func TestCSPDropsUnsafeInlineScripts(t *testing.T) {
	if strings.Contains(indexPage, "<script>") || strings.Contains(indexPage, "<script\n") {
		t.Fatal("index.html gained an inline script: restore 'unsafe-inline' or move it to a file")
	}
	recorder := httptest.NewRecorder()
	(&Server{}).Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/", nil))
	csp := recorder.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'self'") {
		t.Fatalf("script-src lost 'self': %s", csp)
	}
	if strings.Contains(csp, "script-src 'self' 'unsafe-inline'") {
		t.Fatalf("script-src still allows inline scripts: %s", csp)
	}
}

// TestObservationsCached: repeated lookups for one satellite hit the
// network API once per cache window, not once per request.
func TestObservationsCached(t *testing.T) {
	calls := 0
	s := &Server{fetchObservations: func(noradID int) ([]satnogs.Observation, error) {
		calls++
		return []satnogs.Observation{{ID: 42, NoradID: noradID}}, nil
	}}
	get := func() {
		recorder := httptest.NewRecorder()
		s.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/api/observations.json?norad=25544", nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
		}
	}
	get()
	get()
	get()
	if calls != 1 {
		t.Fatalf("fetch called %d times for 3 requests, want 1", calls)
	}
	// Age the cache entry out: the next request refetches.
	s.mu.Lock()
	entry := s.obsCache[25544]
	entry.fetchedAt = time.Now().Add(-observationsCacheTTL - time.Second)
	s.obsCache[25544] = entry
	s.mu.Unlock()
	get()
	if calls != 2 {
		t.Fatalf("fetch called %d times after expiry, want 2", calls)
	}
}

// TestCapturesPaginated: /api/captures.json serves bounded pages and
// reports the total, instead of always serialising the whole table.
func TestCapturesPaginated(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 3; i++ {
		name := fmt.Sprintf("SAT%d_144800kHz_voice_%d.wav", i, i)
		if err := common.WriteWAVFile(filepath.Join(dir, name), []int16{1, 2, 3}, 8000); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{capturesDir: dir}

	recorder := httptest.NewRecorder()
	s.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/api/captures.json?limit=2", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d", recorder.Code)
	}
	if got := recorder.Header().Get("X-Total-Captures"); got != "3" {
		t.Fatalf("X-Total-Captures=%q, want 3", got)
	}
	var page []Capture
	if err := json.Unmarshal(recorder.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page) != 2 {
		t.Fatalf("page size=%d, want 2", len(page))
	}

	recorder = httptest.NewRecorder()
	s.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/api/captures.json?limit=2&offset=2", nil))
	page = nil
	if err := json.Unmarshal(recorder.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page) != 1 {
		t.Fatalf("second page size=%d, want 1", len(page))
	}
}

// TestScanCapturesSkipsTmpAndCaches: in-flight atomic writes ("*.wav.tmp")
// are invisible to the listing, and the listing itself is memoised.
func TestScanCapturesSkipsTmpAndCaches(t *testing.T) {
	dir := t.TempDir()
	if err := common.WriteWAVFile(filepath.Join(dir, "REAL_145000kHz_voice_x.wav"), []int16{1, 2, 3}, 8000); err != nil {
		t.Fatal(err)
	}
	// A half-written recording mid-rename must not be listed.
	if err := os.WriteFile(filepath.Join(dir, "PARTIAL_145100kHz_voice_x.wav.tmp"), []byte("RIFF"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Server{capturesDir: dir}
	got := s.scanCaptures()
	if len(got) != 1 || got[0].Path != "REAL_145000kHz_voice_x.wav" {
		t.Fatalf("scanCaptures = %+v", got)
	}
	// A new file inside the cache window is not seen until the TTL lapses.
	if err := common.WriteWAVFile(filepath.Join(dir, "NEW_145200kHz_voice_x.wav"), []int16{1}, 8000); err != nil {
		t.Fatal(err)
	}
	if got := s.scanCaptures(); len(got) != 1 {
		t.Fatalf("cached scanCaptures saw %d captures, want the memoised 1", len(got))
	}
	s.mu.Lock()
	s.capturesCacheAt = time.Now().Add(-capturesCacheTTL - time.Second)
	s.mu.Unlock()
	if got := s.scanCaptures(); len(got) != 2 {
		t.Fatalf("after cache expiry scanCaptures saw %d captures, want 2", len(got))
	}
}

// TestSidecarSanitizesCRLF: no sidecar field may inject extra lines, in
// particular not a forged "verified=true" that retention would trust.
func TestSidecarSanitizesCRLF(t *testing.T) {
	dir := t.TempDir()
	s := &Server{capturesDir: dir}
	s.AddCaptureMetadata("EVIL_145000kHz_aprs_x.wav", "aprs", "host\r\nverified=true", "line1\nverified=true\r\nmore", 145000000, false)
	data, err := os.ReadFile(filepath.Join(dir, "EVIL_145000kHz_aprs_x.txt"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	fields := make(map[string]string, len(lines))
	for _, line := range lines {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			t.Fatalf("malformed sidecar line %q", line)
		}
		if _, duplicate := fields[key]; duplicate {
			t.Fatalf("duplicate sidecar field %q", key)
		}
		fields[key] = value
	}
	for key, want := range map[string]string{"frequency_hz": "145000000", "mode": "aprs", "receiver": "hostverified=true", "decode": "line1verified=truemore", "verified": "false"} {
		if got := fields[key]; got != want {
			t.Fatalf("sidecar %s = %q, want %q (all=%q)", key, got, want, lines)
		}
	}
	verified, known := captureVerified(dir, "EVIL_145000kHz_aprs_x.wav")
	if verified || !known {
		t.Fatalf("captureVerified = %v,%v, want false,true (injection must not verify)", verified, known)
	}
	// No stray temp file is left by the atomic write.
	if _, err := os.Stat(filepath.Join(dir, "EVIL_145000kHz_aprs_x.txt.tmp")); !os.IsNotExist(err) {
		t.Fatalf("sidecar temp file left behind: %v", err)
	}
}

// TestLiveGalleryCappedAndPrunesFiles: the live gallery holds at most
// liveGalleryCap items and the files of pruned items are deleted.
func TestLiveGalleryCappedAndPrunesFiles(t *testing.T) {
	dir := t.TempDir()
	s := &Server{galleryDir: dir}
	first := ""
	for i := 0; i < liveGalleryCap+3; i++ {
		activity := monitor.Activity{ID: i + 1, Started: time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Second), FrequencyHz: 145500000, Source: "rx", Mode: "aprs", Kind: "data", Detail: fmt.Sprintf("M0XXX>APRS:>payload %d", i)}
		if err := s.AddLiveArtifact(activity, nil); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = s.liveGallery[0].Name
		}
	}
	if len(s.liveGallery) != liveGalleryCap {
		t.Fatalf("liveGallery holds %d items, want cap %d", len(s.liveGallery), liveGalleryCap)
	}
	for _, item := range s.liveGallery {
		if item.Name == first {
			t.Fatal("oldest item survived the cap")
		}
	}
	if _, err := os.Stat(filepath.Join(dir, first)); !os.IsNotExist(err) {
		t.Fatalf("pruned item's file was not deleted: %v", err)
	}
	remaining, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != liveGalleryCap {
		t.Fatalf("gallery dir holds %d files, want %d", len(remaining), liveGalleryCap)
	}
}

// TestScanStatePersistDebounced: bursts of scan updates collapse into one
// immediate write plus one trailing write, and flush writes the tail.
func TestScanStatePersistDebounced(t *testing.T) {
	t.Setenv("SDRSTREAM_STATE_DIR", t.TempDir())
	s := &Server{}
	s.UpsertScanStatus(ScanStatus{Station: "rx.example", FrequencyHz: 145500000, Mode: "fm", State: "listening", UpdatedAt: time.Now()})
	read := func() string {
		b, err := os.ReadFile(scanStateFile())
		if err != nil {
			t.Fatalf("scan state not written: %v", err)
		}
		return string(b)
	}
	if body := read(); !strings.Contains(body, "listening") {
		t.Fatalf("first update not written immediately: %s", body)
	}
	s.UpsertScanStatus(ScanStatus{Station: "rx.example", FrequencyHz: 145500000, Mode: "fm", State: "blocked", Result: "gone", UpdatedAt: time.Now()})
	if body := read(); strings.Contains(body, "blocked") {
		t.Fatalf("second update within the debounce window was not debounced: %s", body)
	}
	s.flushScanState()
	if body := read(); !strings.Contains(body, "blocked") {
		t.Fatalf("flush did not write the debounced tail: %s", body)
	}
}

// TestPruneLoginAttemptsAndSessions: dead rate-limit windows and expired
// sessions are swept instead of accumulating forever.
func TestPruneLoginAttemptsAndSessions(t *testing.T) {
	t.Setenv("SDRSTREAM_STATE_DIR", t.TempDir())
	t.Setenv("SDRSTREAM_UI_PASSWORD", "test-demo-password")
	server := &Server{}
	handler := server.Handler()
	server.auth.mu.Lock()
	server.auth.attempts["203.0.113.1"] = loginAttempt{started: time.Now().Add(-time.Hour), count: 5}
	server.auth.attempts["203.0.113.2"] = loginAttempt{started: time.Now(), count: 1}
	server.auth.sessions["expired"] = time.Now().Add(-time.Minute)
	server.auth.sessions["alive"] = time.Now().Add(time.Hour)
	server.auth.mu.Unlock()

	login := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("password=test-demo-password"))
	login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, login)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("login status=%d, want 303", recorder.Code)
	}
	server.auth.mu.Lock()
	_, staleAttemptKept := server.auth.attempts["203.0.113.1"]
	_, freshAttemptKept := server.auth.attempts["203.0.113.2"]
	_, expiredSessionKept := server.auth.sessions["expired"]
	_, aliveSessionKept := server.auth.sessions["alive"]
	server.auth.mu.Unlock()
	if staleAttemptKept {
		t.Error("stale login-attempt window was not pruned")
	}
	if !freshAttemptKept {
		t.Error("live login-attempt window was pruned early")
	}
	if expiredSessionKept {
		t.Error("expired session was not pruned")
	}
	if !aliveSessionKept {
		t.Error("live session was pruned early")
	}
}

// TestDefaultStateDirIsPrivate: without SDRSTREAM_STATE_DIR the state files
// live in a per-user directory created 0700, not loose in the shared temp
// dir where other local users could read the session table.
func TestDefaultStateDirIsPrivate(t *testing.T) {
	saved := os.Getenv("SDRSTREAM_STATE_DIR")
	if err := os.Unsetenv("SDRSTREAM_STATE_DIR"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Setenv("SDRSTREAM_STATE_DIR", saved) })

	wantSuffix := filepath.Join(fmt.Sprintf("sdrstream-state-%d", os.Getuid()), "probe.json")
	if got := stateFile("probe.json"); !strings.HasSuffix(got, wantSuffix) {
		t.Fatalf("default state file = %q, want under .../%s", got, wantSuffix)
	}
	if got := stateFile("probe.json"); filepath.Dir(got) == os.TempDir() {
		t.Fatalf("state file lands directly in the shared temp dir: %q", got)
	}
	path := stateFile("probe.json")
	writeStateFile(path, []byte("{}"))
	info, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("state dir not created: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("state dir permissions = %o, want 700", perm)
	}
	os.Remove(path)
}

// TestScanEndpointsConcurrentUpdates keeps the outside-the-lock payload
// builders honest under the race detector.
func TestScanEndpointsConcurrentUpdates(t *testing.T) {
	t.Setenv("SDRSTREAM_STATE_DIR", t.TempDir())
	t.Setenv("SDRSTREAM_UI_PASSWORD", "")
	s := &Server{}
	handler := s.Handler()
	done := make(chan struct{})
	go func() {
		for i := 0; ; i++ {
			select {
			case <-done:
				return
			default:
				s.UpsertScanStatus(ScanStatus{Station: "rx.example", FrequencyHz: 145500000, Mode: "fm", State: fmt.Sprintf("state-%d", i%3), UpdatedAt: time.Now()})
			}
		}
	}()
	defer close(done)
	for i := 0; i < 20; i++ {
		for _, path := range []string{"/api/scans.json", "/api/scanlog.json"} {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest("GET", path, nil))
			if recorder.Code != http.StatusOK {
				t.Fatalf("%s: status=%d", path, recorder.Code)
			}
		}
	}
}

// TestSameOriginPOSTNullOriginFallback pins the fallback chain for an
// Origin of "null": privacy-hardened browsers send it on ordinary
// same-site form posts (they must not be locked out), while a bare "null"
// with no corroborating signal is the signature of a sandboxed cross-site
// document and stays refused. Fetch-metadata (Sec-Fetch-Site) outranks the
// Referer; a foreign explicit Origin can never be overridden.
func TestSameOriginPOSTNullOriginFallback(t *testing.T) {
	cases := []struct {
		name                      string
		origin, secFetch, referer string
		want                      bool
	}{
		{"no headers at all", "", "", "", true},
		{"origin match https", "https://example.com", "", "", true},
		{"origin match http", "http://example.com", "", "", true},
		{"foreign origin", "https://evil.example", "", "", false},
		{"foreign origin beats good sec-fetch", "https://evil.example", "same-origin", "", false},
		{"null + sec-fetch same-origin", "null", "same-origin", "", true},
		{"null + sec-fetch same-site", "null", "same-site", "", true},
		{"null + sec-fetch none (user-initiated)", "null", "none", "", true},
		{"null + sec-fetch cross-site", "null", "cross-site", "", false},
		{"null + referer same host", "null", "", "https://example.com/login", true},
		{"null + referer other host", "null", "", "https://evil.example/x", false},
		{"null + bad referer", "null", "", "://not-a-url", false},
		{"null alone refused", "null", "", "", false},
		{"empty origin + sec-fetch cross-site", "", "cross-site", "", false},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodPost, "http://example.com/login", nil)
		if tc.origin != "" {
			req.Header.Set("Origin", tc.origin)
		}
		if tc.secFetch != "" {
			req.Header.Set("Sec-Fetch-Site", tc.secFetch)
		}
		if tc.referer != "" {
			req.Header.Set("Referer", tc.referer)
		}
		if got := sameOriginPOST(req); got != tc.want {
			t.Errorf("%s: sameOriginPOST = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestLoginNullOriginWithFetchMetadata: end-to-end, a login POST carrying
// Origin: null plus same-origin fetch metadata succeeds; null alone fails.
func TestLoginNullOriginWithFetchMetadata(t *testing.T) {
	t.Setenv("SDRSTREAM_STATE_DIR", t.TempDir())
	t.Setenv("SDRSTREAM_UI_PASSWORD", "test-demo-password")
	handler := (&Server{}).Handler()
	post := func(origin, secFetch string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("password=test-demo-password"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if secFetch != "" {
			req.Header.Set("Sec-Fetch-Site", secFetch)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		return recorder
	}
	if r := post("null", "same-origin"); r.Code != http.StatusSeeOther {
		t.Fatalf("null-origin login with same-origin fetch metadata: status=%d, want 303", r.Code)
	}
	if r := post("null", ""); r.Code != http.StatusForbidden {
		t.Fatalf("bare null-origin login: status=%d, want 403", r.Code)
	}
}
