package sdrshare

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSessionIDHashesCookie(t *testing.T) {
	id := SessionID("secret-token-value")
	if id == "" || strings.Contains(id, "secret") || IsInternal(id) || len(id) != 18 {
		t.Fatalf("bad id %q", id)
	}
	if SessionID("secret-token-value") != id || SessionID("other") == id || SessionID("") != "" {
		t.Fatal("id not stable/distinct/empty-safe")
	}
}

func TestRequestSession(t *testing.T) {
	r := httptest.NewRequest("POST", "/", nil)
	if RequestSession(r, true) != "" {
		t.Fatal("auth on, no cookie must deny")
	}
	if RequestSession(r, false) != LoopbackSession {
		t.Fatal("open mode loopback")
	}
	r.AddCookie(&http.Cookie{Name: "sdrstream_session", Value: "tok"})
	if got := RequestSession(r, true); got != SessionID("tok") {
		t.Fatalf("got %q", got)
	}
	// a cookie value that mimics the internal prefix is still hashed
	r2 := httptest.NewRequest("POST", "/", nil)
	r2.AddCookie(&http.Cookie{Name: "sdrstream_session", Value: "internal:obs"})
	if IsInternal(RequestSession(r2, true)) {
		t.Fatal("internal prefix spoofable via cookie")
	}
}

func TestSourceKey(t *testing.T) {
	if SourceKey("Kiwi", " Host:8073/ ", "p") != SourceKey("kiwi", "host:8073", "p") {
		t.Fatal("not normalised")
	}
}
