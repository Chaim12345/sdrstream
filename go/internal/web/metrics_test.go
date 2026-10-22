package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthzAndMetrics(t *testing.T) {
	t.Setenv("SDRSTREAM_UI_PASSWORD", "pw-for-test")
	s := New(t.TempDir(), t.TempDir())
	h := s.Handler()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/healthz", nil))
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"ok":true`) {
		t.Fatalf("healthz = %d %q", rr.Code, rr.Body.String())
	}
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/api/metrics.json", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("metrics without session = %d, want 401", rr.Code)
	}
}
