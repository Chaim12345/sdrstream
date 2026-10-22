package sources

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const sampleTLE = `ISS (ZARYA)
1 25544U 98067A   26295.50000000  .00016717  00000-0  10270-3 0  9000
2 25544  51.6400 208.9163 0006703  69.9862  25.2906 15.49560532  1000
`

func TestTLEEpoch(t *testing.T) {
	stamp, ok := TLEEpoch([]byte(sampleTLE))
	if !ok {
		t.Fatal("no epoch parsed")
	}
	want := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	if !stamp.Equal(want) {
		t.Fatalf("epoch %s, want %s", stamp, want)
	}
	if _, ok := TLEEpoch([]byte("no tle here")); ok {
		t.Fatal("parsed an epoch from garbage")
	}
}

func TestKIndexTime(t *testing.T) {
	body := `[{"time_tag":"2026-10-22T03:00:00","Kp":2.0},{"time_tag":"2026-10-22T06:00:00","Kp":1.67}]`
	stamp, ok := KIndexTime([]byte(body))
	if !ok {
		t.Fatal("no time parsed")
	}
	if stamp.Hour() != 6 {
		t.Fatalf("newest %s, want 06:00", stamp)
	}
	if _, ok := KIndexTime([]byte("not json")); ok {
		t.Fatal("parsed time from garbage")
	}
}

func TestProbeLiveStaleAndForced(t *testing.T) {
	fresh := time.Now().UTC().Add(-time.Hour)
	old := time.Now().UTC().Add(-30 * 24 * time.Hour)
	tle := func(epoch time.Time) string {
		year := epoch.Year() % 100
		day := float64(epoch.YearDay()) + float64(epoch.Hour())/24
		return fmt.Sprintf("ISS (ZARYA)\n1 25544U 98067A   %02d%012.8f  .00016717  00000-0  10270-3 0  9000\n", year, day)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/fresh":
			fmt.Fprint(w, tle(fresh))
		case "/old":
			fmt.Fprint(w, tle(old))
		case "/restricted":
			fmt.Fprint(w, "human page only")
		case "/broken":
			fmt.Fprint(w, "something else entirely")
		default:
			http.Error(w, "gone", http.StatusBadGateway)
		}
	}))
	defer server.Close()
	cases := []struct {
		name   string
		source Source
		want   Status
	}{
		{"fresh", Source{Name: "a", URL: server.URL + "/fresh", Match: "1 25544U",
			FreshFor: 10 * 24 * time.Hour, ExtractTime: TLEEpoch}, StatusLive},
		{"stale", Source{Name: "b", URL: server.URL + "/old", Match: "1 25544U",
			FreshFor: 10 * 24 * time.Hour, ExtractTime: TLEEpoch}, StatusStale},
		{"restricted", Source{Name: "c", URL: server.URL + "/restricted", Forced: StatusRestricted,
			Match: "human"}, StatusRestricted},
		{"content-missing", Source{Name: "d", URL: server.URL + "/broken", Match: "expected"}, StatusUnreachable},
		{"http-error", Source{Name: "e", URL: server.URL + "/error"}, StatusUnreachable},
		{"dns-failure", Source{Name: "f", URL: "http://nonexistent.invalid/"}, StatusUnreachable},
	}
	for _, tc := range cases {
		result := probe(context.Background(), tc.source, 5*time.Second)
		if result.Status != tc.want {
			t.Errorf("%s: status %s (%s), want %s", tc.name, result.Status, result.Detail, tc.want)
		}
		if tc.want == StatusStale && !strings.Contains(result.Detail, "old") {
			t.Errorf("stale detail should carry the data age, got %q", result.Detail)
		}
	}
}

func TestHealthAllPoliteness(t *testing.T) {
	var inFlight int64
	var maxSameHost int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := atomic.AddInt64(&inFlight, 1)
		for {
			peak := atomic.LoadInt64(&maxSameHost)
			if current <= peak || atomic.CompareAndSwapInt64(&maxSameHost, peak, current) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		fmt.Fprint(w, "ok marker")
		atomic.AddInt64(&inFlight, -1)
	}))
	defer server.Close()
	registry := make([]Source, 4)
	for i := range registry {
		registry[i] = Source{Name: fmt.Sprintf("s%d", i), URL: server.URL + "/", Match: "marker"}
	}
	saved := Registry
	Registry = registry
	defer func() { Registry = saved }()
	results := HealthAll(context.Background(), Options{Timeout: 5 * time.Second, Concurrency: 4})
	if len(results) != 4 {
		t.Fatalf("got %d results, want 4", len(results))
	}
	for _, result := range results {
		if result.Status != StatusLive {
			t.Errorf("%s: %s (%s)", result.Name, result.Status, result.Detail)
		}
	}
	if max := atomic.LoadInt64(&maxSameHost); max > 1 {
		t.Errorf("per-host politeness violated: %d requests in flight on one host", max)
	}
}

func TestHealthAllKindFilter(t *testing.T) {
	saved := Registry
	Registry = []Source{
		{Name: "one", Kind: "alpha", URL: "http://nonexistent.invalid/"},
		{Name: "two", Kind: "beta", URL: "http://nonexistent.invalid/"},
	}
	defer func() { Registry = saved }()
	results := HealthAll(context.Background(), Options{Timeout: 2 * time.Second, Kinds: []string{"alpha"}})
	if len(results) != 1 || results[0].Name != "one" {
		t.Fatalf("kind filter returned %+v", results)
	}
}

