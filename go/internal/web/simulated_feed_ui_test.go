package web

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/monitor"
)

// TestSimulatedFeedUIServer serves the REAL UI over httptest with an
// honest simulated feed (one entry per verdict state: passed with its
// gallery twin, rejected, ungated, historical) behind a permanent
// "SIMULATED FEED TEST - no live reception" banner, so the feed
// semantics can be pixel-verified without seeding any production demo
// with synthetic-as-live data. It runs only with SDRSTREAM_SIM_UI=1
// and blocks until the stop file appears (default /tmp/sim_ui_stop).
// The pill expectation in this simulated scenario is 1 - a simulated
// test expectation, not a claim about real activity.
func TestSimulatedFeedUIServer(t *testing.T) {
	if os.Getenv("SDRSTREAM_SIM_UI") == "" {
		t.Skip("pixel-verification server; set SDRSTREAM_SIM_UI=1 to run")
	}
	now := time.Now().UTC().Truncate(time.Second)
	m := &monitor.Monitor{}
	passedID := m.AddActivity(monitor.Activity{Started: now, FrequencyHz: 14491234.7, Kind: "sstv", Mode: "sstv", LevelDB: 18.2, Source: "simulated"})
	rejectedID := m.AddActivity(monitor.Activity{Started: now.Add(-time.Minute), FrequencyHz: 7030000, Kind: "cw", Mode: "cw", LevelDB: 9.6, Detail: "CQ DE SIM", Source: "simulated"})
	m.AddActivity(monitor.Activity{Started: now.Add(-2 * time.Minute), FrequencyHz: 145800000, Kind: "unknown", LevelDB: 6.1, Source: "simulated"})
	m.RecordGate(passedID, true, "live gate passed")
	m.RecordGate(rejectedID, false, "text failed plausibility (simulated)")
	s := &Server{}
	s.monitor = m
	s.liveGallery = []LiveGalleryItem{
		// Twin of the passed decode: merges into ONE counted entry.
		{Source: "live", Station: "simulated", Start: now, Kind: "image", Mode: "sstv", Name: "sim-twin.png", FrequencyHz: 14491234, Verification: "simulated gate reason - not a reception"},
		// Legacy live item with no gate-owned verdict: historical.
		{Source: "live", Station: "simulated", Start: now.Add(-time.Hour), Kind: "image", Mode: "sstv", Name: "sim-legacy.png", FrequencyHz: 14491000},
		// Synthetic proof is not live provenance: never in the feed.
		{Source: "test-fixture", Station: "simulated", Start: now.Add(-2 * time.Hour), Kind: "image", Mode: "sstv", Name: "sim-fixture.png", Verification: "synthetic proof"},
	}
	inner := s.Handler()
	const banner = `<div style="background:#b91c1c;color:#fff;font:bold 16px/2.4 sans-serif;text-align:center;letter-spacing:.05em">SIMULATED FEED TEST - no live reception</div>`
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			data, err := staticFS.ReadFile("static/index.html")
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			page := string(data)
			if i := strings.Index(page, "<body>"); i >= 0 {
				page = page[:i+6] + banner + page[i+6:]
			} else {
				page = banner + page
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, page)
			return
		}
		inner.ServeHTTP(w, r)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	fmt.Println("SIM_UI_URL=" + srv.URL)
	_ = os.WriteFile("/tmp/sim_ui_url", []byte(srv.URL), 0o644)
	stop := os.Getenv("SDRSTREAM_SIM_UI_STOP")
	if stop == "" {
		stop = "/tmp/sim_ui_stop"
	}
	for {
		if _, err := os.Stat(stop); err == nil {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}
