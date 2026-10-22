package web

import (
	"net/http"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/feeds"
)

var processStart = time.Now()

// handleHealthz is a liveness probe for watchdogs and tunnels. It is
// reachable without a session and deliberately reveals nothing but
// "the process is serving".
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(`{"ok":true}` + "\n"))
}

// MetricsPayload is the /api/metrics.json body: real throughput numbers
// observed by this process since it started.
type MetricsPayload struct {
	GeneratedAt   time.Time            `json:"generated_at"`
	UptimeSeconds int64                `json:"uptime_seconds"`
	Feeds         *feeds.MetricsReport `json:"feeds,omitempty"`
	Captures      int                  `json:"captures"`
	ScanStations  int                  `json:"scan_stations"`
	ScanOutcomes  map[string]int       `json:"scan_outcomes"`
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	scanned := s.scanCaptures()
	s.mu.Lock()
	p := MetricsPayload{
		GeneratedAt:   time.Now(),
		UptimeSeconds: int64(time.Since(processStart).Seconds()),
		Captures:      len(scanned) + len(s.captures),
		ScanOutcomes:  map[string]int{},
	}
	for _, st := range s.scans {
		p.ScanStations++
		p.ScanOutcomes[st.State]++
	}
	m := s.feeds
	s.mu.Unlock()
	if m != nil {
		rep := m.Metrics()
		p.Feeds = &rep
	}
	writeJSON(w, p)
}
