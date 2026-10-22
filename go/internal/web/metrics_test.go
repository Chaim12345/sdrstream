package web

import (
	"github.com/chaim12345/sdrstream/go/internal/feeds"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestLedgerEndpointAndCSVInjectionGuard(t *testing.T) {
	t.Setenv("SDRSTREAM_UI_PASSWORD", "")
	s := New(t.TempDir(), t.TempDir())
	s.NoAuth = true
	m := feeds.New(10, func(string, ...any) {})
	l := feeds.NewLedger(filepath.Join(t.TempDir(), "l.jsonl"), 0)
	m.SetLedger(l)
	s.SetFeeds(m)
	m.Add(feeds.Item{ID: "1", Source: "s", Kind: "k", Label: "=HYPERLINK(\"x\")", Time: time.Now()})
	l.Close()
	h := s.Handler()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/api/ledger.csv", nil))
	body := rr.Body.String()
	if !strings.Contains(body, `'=HYPERLINK`) || strings.Contains(body, ",=HYPERLINK") {
		t.Fatalf("csv not neutralised: %q", body)
	}
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/api/ledger.json?source=s", nil))
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"decoded_by": "provider"`) {
		t.Fatalf("json = %d %s", rr.Code, rr.Body.String())
	}
}

func TestFeedCardsShowProofDetails(t *testing.T) {
	b, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(b)
	for _, want := range []string{"pb.textContent='proof'", "decoded by", "fetched_at"} {
		if !strings.Contains(js, want) {
			t.Fatalf("app.js lacks %q", want)
		}
	}
}

func TestFeedsNoteFlagsStaleSources(t *testing.T) {
	b, _ := staticFS.ReadFile("static/app.js")
	if !strings.Contains(string(b), "STALE, last fetch") {
		t.Fatal("stale-source marker missing from app.js")
	}
}

func TestUITermsLinePinned(t *testing.T) {
	b, _ := staticFS.ReadFile("static/app.js")
	for _, want := range []string{"'Licence: '", "terms_notes", "max_age_seconds"} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("app.js lacks %q", want)
		}
	}
}
