package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func sharedServer(t *testing.T) (*Server, *int, *int) {
	t.Helper()
	t.Setenv("SDRSTREAM_UI_PASSWORD", "pw-for-test")
	s := &Server{}
	s.auth.mu.Lock()
	s.auth.sessions = map[string]time.Time{
		"tokA": time.Now().Add(time.Hour),
		"tokB": time.Now().Add(time.Hour),
		"tokX": time.Now().Add(-time.Hour), // expired
	}
	s.auth.mu.Unlock()
	starts, stops := 0, 0
	s.SetMonitorHooks(MonitorHooks{
		Start: func(MonitorStartSpec) error { starts++; return nil },
		Stop:  func() error { stops++; return nil },
	})
	return s, &starts, &stops
}

func call(s *Server, tok, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "http://example.test"+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://example.test")
	if tok != "" {
		req.AddCookie(&http.Cookie{Name: "sdrstream_session", Value: tok})
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

const startBody = "action=start&server=rx.example:8073&kind=openwebrx"

func TestSharedStartJoinTakeover(t *testing.T) {
	s, starts, stops := sharedServer(t)
	if r := call(s, "tokA", "/api/monitor/control", startBody); r.Code != 200 || *starts != 1 {
		t.Fatalf("A start: %d %s", r.Code, r.Body)
	}
	// B cannot silently replace A's receiver.
	if r := call(s, "tokB", "/api/monitor/control", startBody); r.Code != http.StatusConflict || *starts != 1 {
		t.Fatalf("B start without takeover: %d starts=%d", r.Code, *starts)
	}
	// B joins: no hook call, no dial.
	if r := call(s, "tokB", "/api/monitor/join", ""); r.Code != 200 || *starts != 1 {
		t.Fatalf("B join: %d %s", r.Code, r.Body)
	}
	// Joiner cannot tune or change controls.
	if r := call(s, "tokB", "/api/tune.json", "frequency_hz=7000000"); r.Code != http.StatusForbidden {
		t.Fatalf("joiner tune: %d", r.Code)
	}
	if r := call(s, "tokB", "/api/monitor/control", "step_hz=5000"); r.Code != http.StatusForbidden {
		t.Fatalf("joiner control: %d", r.Code)
	}
	// Controller stops: joiner keeps the receiver alive.
	call(s, "tokA", "/api/monitor/control", "action=stop")
	if *stops != 0 {
		t.Fatal("receiver closed while a listener remains")
	}
	// Last one out closes it.
	call(s, "tokB", "/api/monitor/control", "action=stop")
	if *stops != 1 {
		t.Fatalf("stops=%d", *stops)
	}
}

func TestSharedTakeoverOnlyWhenExplicit(t *testing.T) {
	s, starts, _ := sharedServer(t)
	call(s, "tokA", "/api/monitor/control", startBody)
	r := call(s, "tokB", "/api/monitor/control", startBody+"&takeover=1")
	if r.Code != 200 || *starts != 2 {
		t.Fatalf("explicit takeover: %d starts=%d", r.Code, *starts)
	}
}

func TestSharedRejectsUnvalidatedPrincipal(t *testing.T) {
	s, starts, _ := sharedServer(t)
	for _, tok := range []string{"", "tokX", "forged"} {
		// With a password set wrapAuth already rejects; check the gate itself
		// too, by calling it directly past the middleware.
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		if tok != "" {
			req.AddCookie(&http.Cookie{Name: "sdrstream_session", Value: tok})
		}
		if _, ok := s.principal(req); ok {
			t.Fatalf("token %q accepted", tok)
		}
		if r := call(s, tok, "/api/monitor/control", startBody); r.Code == 200 {
			t.Fatalf("token %q started a receiver", tok)
		}
	}
	if *starts != 0 {
		t.Fatal("hook ran for an unauthenticated caller")
	}
}

func TestSharedStartFailureUnwindsClaim(t *testing.T) {
	s, _, _ := sharedServer(t)
	s.SetMonitorHooks(MonitorHooks{Start: func(MonitorStartSpec) error { return http.ErrAbortHandler }})
	if r := call(s, "tokA", "/api/monitor/control", startBody); r.Code != http.StatusBadGateway {
		t.Fatalf("status %d", r.Code)
	}
	if n := len(s.sharedHub().Active()); n != 0 {
		t.Fatalf("claim leaked: %d active", n)
	}
}

func TestOpenModeSingleSessionNoConflict(t *testing.T) {
	t.Setenv("SDRSTREAM_UI_PASSWORD", "")
	s := &Server{NoAuth: true}
	s.SetMonitorHooks(MonitorHooks{Start: func(MonitorStartSpec) error { return nil }, Stop: func() error { return nil }})
	for i := 0; i < 2; i++ {
		if r := call(s, "", "/api/monitor/control", startBody); r.Code != 200 {
			t.Fatalf("open start %d: %d %s", i, r.Code, r.Body)
		}
	}
}
