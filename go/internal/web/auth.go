package web

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// sessionTTL is how long a login lasts; sessionRenewWindow is how close to
// expiry a session must be before a request slides it forward again.
const (
	sessionTTL         = time.Hour
	sessionRenewWindow = 30 * time.Minute
)

type loginAttempt struct {
	started time.Time
	count   int
}

// authState holds the per-server login and session state. It lives on the
// Server, not in package globals, so two servers (and tests) never share
// sessions or lockouts.
type authState struct {
	mu       sync.Mutex
	sessions map[string]time.Time
	attempts map[string]loginAttempt
}

func (a *authState) init() {
	a.mu.Lock()
	if a.sessions == nil {
		a.sessions = map[string]time.Time{}
	}
	if a.attempts == nil {
		a.attempts = map[string]loginAttempt{}
	}
	if len(a.sessions) == 0 {
		if b, err := os.ReadFile(authSessionFile()); err == nil {
			if loaded := map[string]time.Time{}; json.Unmarshal(b, &loaded) == nil {
				// Drop sessions that expired while the process was down so a
				// long-offline server does not reload dead entries.
				for value, expires := range loaded {
					if time.Now().Before(expires) {
						a.sessions[value] = expires
					}
				}
			}
		}
	}
	a.mu.Unlock()
}

// clientIP keys rate limits on the real client. Behind a local cloudflared
// quick tunnel every request arrives from 127.0.0.1, so only when the
// direct peer is loopback do we trust CF-Connecting-IP; from anywhere else
// that header is attacker-controlled and ignored.
func clientIP(r *http.Request) string {
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		host = h
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if cf := strings.TrimSpace(r.Header.Get("CF-Connecting-IP")); cf != "" {
			if cfIP := net.ParseIP(cf); cfIP != nil {
				return cfIP.String()
			}
		}
	}
	return host
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: "sdrstream_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
}

func sessionCookie(value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name: "sdrstream_session", Value: value, Path: "/", MaxAge: maxAge,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
	}
}

// handleLogin serves the password form and creates sessions. Wrong
// passwords are rate-limited per client IP: 5 tries per 15 minutes.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	uiPassword := os.Getenv("SDRSTREAM_UI_PASSWORD")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if r.Method == http.MethodGet {
		fmt.Fprint(w, `<!doctype html><html lang="en"><meta name="viewport" content="width=device-width,initial-scale=1"><title>sdrstream login</title><style>body{background:#0a0d14;color:#d9e1ef;font:16px system-ui;display:grid;place-items:center;min-height:90vh}form{background:#11151f;padding:24px;border:1px solid #1f2635;border-radius:12px;width:min(360px,85vw)}input,button{box-sizing:border-box;width:100%;padding:12px;margin-top:12px;border-radius:6px}input{background:#161b28;color:#fff;border:1px solid #455}button{background:#5cc8ff;border:0;font-weight:600}</style><form method="post" action="/login"><h1>sdrstream</h1><label for="password">Demo password</label><input id="password" name="password" type="password" autocomplete="current-password" required autofocus><button type="submit">Open monitor</button></form></html>`)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if uiPassword == "" {
		http.Error(w, "login is not configured", http.StatusServiceUnavailable)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	ip := clientIP(r)
	s.auth.mu.Lock()
	attempt, found := s.auth.attempts[ip]
	if found && time.Since(attempt.started) > 15*time.Minute {
		delete(s.auth.attempts, ip)
		attempt, found = s.auth.attempts[ip]
	}
	if !found {
		attempt = loginAttempt{started: time.Now()}
	}
	if attempt.count >= 5 {
		s.auth.mu.Unlock()
		w.Header().Set("Retry-After", "900")
		http.Error(w, "too many attempts", http.StatusTooManyRequests)
		return
	}
	attempt.count++
	s.auth.attempts[ip] = attempt
	s.auth.mu.Unlock()
	if subtle.ConstantTimeCompare([]byte(r.FormValue("password")), []byte(uiPassword)) != 1 {
		http.Error(w, "invalid password", http.StatusUnauthorized)
		return
	}
	s.auth.mu.Lock()
	delete(s.auth.attempts, ip)
	s.auth.mu.Unlock()
	var session [32]byte
	if _, err := rand.Read(session[:]); err != nil {
		http.Error(w, "could not create session", http.StatusInternalServerError)
		return
	}
	sessionValue := fmt.Sprintf("%x", session[:])
	s.auth.mu.Lock()
	s.auth.sessions[sessionValue] = time.Now().Add(sessionTTL)
	s.persistSessionsLocked()
	s.auth.mu.Unlock()
	http.SetCookie(w, sessionCookie(sessionValue, 3600))
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handleLogout ends the session. It is POST-only so a third-party page
// cannot log the user out with an image tag or a link prefetch.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "logout requires POST", http.StatusMethodNotAllowed)
		return
	}
	clearSessionCookie(w)
	if cookie, err := r.Cookie("sdrstream_session"); err == nil {
		s.auth.mu.Lock()
		delete(s.auth.sessions, cookie.Value)
		s.persistSessionsLocked()
		s.auth.mu.Unlock()
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// wrapAuth gates every route but /login and /logout behind a session when
// SDRSTREAM_UI_PASSWORD is set. An expired or unknown cookie is cleared and
// the browser is sent back to /login instead of a bare 401; sessions close
// to expiry slide forward so an active user is never cut off mid-session.
func (s *Server) wrapAuth(mux *http.ServeMux) http.Handler {
	uiPassword := os.Getenv("SDRSTREAM_UI_PASSWORD")
	if uiPassword == "" {
		log.Printf("web: WARNING: SDRSTREAM_UI_PASSWORD is not set - the UI and its APIs are served WITHOUT authentication; " +
			"set SDRSTREAM_UI_PASSWORD or pass -no-auth to run openly")
		return mux
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" || r.URL.Path == "/logout" {
			mux.ServeHTTP(w, r)
			return
		}
		resource := strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/audio/") || strings.HasPrefix(r.URL.Path, "/gallery/")
		cookie, err := r.Cookie("sdrstream_session")
		if err != nil {
			if resource {
				http.Error(w, "access denied", http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		renew := false
		s.auth.mu.Lock()
		expires, ok := s.auth.sessions[cookie.Value]
		if ok && time.Now().After(expires) {
			delete(s.auth.sessions, cookie.Value)
			s.persistSessionsLocked()
			ok = false
		}
		if ok && time.Until(expires) < sessionRenewWindow {
			s.auth.sessions[cookie.Value] = time.Now().Add(sessionTTL)
			s.persistSessionsLocked()
			renew = true
		}
		s.auth.mu.Unlock()
		if !ok {
			clearSessionCookie(w)
			if resource {
				http.Error(w, "access denied", http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if renew {
			http.SetCookie(w, sessionCookie(cookie.Value, 3600))
		}
		mux.ServeHTTP(w, r)
	})
}

// stateFile locates a small JSON state file. SDRSTREAM_STATE_DIR overrides
// the directory (use a persistent volume to survive container rebuilds, or a
// test temp dir to isolate runs); the default is the OS temp dir.
func stateFile(name string) string {
	if dir := os.Getenv("SDRSTREAM_STATE_DIR"); dir != "" {
		return filepath.Join(dir, name)
	}
	return filepath.Join(os.TempDir(), name)
}

// writeStateFile atomically persists one JSON state file, creating
// SDRSTREAM_STATE_DIR first when it does not exist yet.
func writeStateFile(path string, b []byte) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return
		}
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, path)
	}
}

func authSessionFile() string { return stateFile("sdrstream-auth-sessions.json") }
func (s *Server) persistSessionsLocked() {
	b, err := json.Marshal(s.auth.sessions)
	if err != nil {
		return
	}
	writeStateFile(authSessionFile(), b)
}
