package web

import (
	"encoding/csv"
	"net/http"
	"strconv"
	"strings"
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

func (s *Server) ledger() *feeds.Ledger {
	s.mu.Lock()
	m := s.feeds
	s.mu.Unlock()
	if m == nil {
		return nil
	}
	return m.Ledger()
}

func ledgerLimit(r *http.Request) int {
	n, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if n <= 0 || n > 5000 {
		return 500
	}
	return n
}

// handleLedger serves the proof ledger: ?source= filters, ?limit= caps
// (default 500, at most 5000). Newest first.
func (s *Server) handleLedger(w http.ResponseWriter, r *http.Request) {
	out := []feeds.LedgerEntry{}
	if l := s.ledger(); l != nil {
		out = l.Query(r.URL.Query().Get("source"), ledgerLimit(r))
	}
	writeJSON(w, map[string]any{"entries": out, "note": "first sightings since the ledger began; item is sdrstream's normalised view, ref links to the provider"})
}

// handleLedgerCSV is the same data as CSV; cells starting with = + - @ are
// prefixed with a quote so spreadsheets do not run provider text as formulas.
func (s *Server) handleLedgerCSV(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="sdrstream-ledger.csv"`)
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"source", "kind", "label", "detail", "provider_time", "fetched_at", "decoded_by", "ref"})
	if l := s.ledger(); l != nil {
		for _, e := range l.Query(r.URL.Query().Get("source"), ledgerLimit(r)) {
			_ = cw.Write([]string{csvSafe(e.Source), csvSafe(e.Kind), csvSafe(e.Label), csvSafe(e.Detail),
				e.ProviderT.UTC().Format(time.RFC3339), e.FetchedAt.UTC().Format(time.RFC3339), csvSafe(e.DecodedBy), csvSafe(e.Ref)})
		}
	}
	cw.Flush()
}

func csvSafe(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}
