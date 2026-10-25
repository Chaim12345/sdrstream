package web

import (
	"net/http"
	"sort"

	"github.com/chaim12345/sdrstream/go/internal/probe"
)

// stationView is one catalog record for /api/station-catalog.json.
type stationView struct {
	Key            string           `json:"key"`
	Kind           string           `json:"kind"`
	Name           string           `json:"name"`
	URL            string           `json:"url"`
	Lat            float64          `json:"lat,omitempty"`
	Lon            float64          `json:"lon,omitempty"`
	Reachable      bool             `json:"reachable"`
	Verdict        string           `json:"verdict"`
	CheckedAt      string           `json:"checked_at,omitempty"`
	ConsecFailures int              `json:"consec_failures,omitempty"`
	Capability     probe.Capability `json:"capability,omitempty"`
}

// SetProbeCatalog wires the probe catalog into the server.
func (s *Server) SetProbeCatalog(c *probe.Catalog) {
	s.mu.Lock()
	s.probeCatalog = c
	s.mu.Unlock()
}

// handleProbeStations serves the station capability catalog, reachable
// stations first: the UI and the decoder fleet read "which stations exist,
// are they alive, what can they hear" from here instead of probing.
func (s *Server) handleProbeStations(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	c := s.probeCatalog
	s.mu.Unlock()
	if c == nil {
		writeJSON(w, map[string]interface{}{"stations": []stationView{}, "enabled": false})
		return
	}
	out := make([]stationView, 0, len(c.Records))
	for _, rec := range c.Records {
		v := stationView{
			Key: rec.Key, Kind: rec.Kind, Name: rec.Name, URL: rec.URL,
			Lat: rec.Lat, Lon: rec.Lon,
			Reachable: rec.Reachable, Verdict: rec.Verdict,
			ConsecFailures: rec.ConsecFailures, Capability: rec.Capability,
		}
		if !rec.CheckedAt.IsZero() {
			v.CheckedAt = rec.CheckedAt.UTC().Format("2006-01-02T15:04:05Z")
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Reachable != out[j].Reachable {
			return out[i].Reachable
		}
		return out[i].Key < out[j].Key
	})
	writeJSON(w, map[string]interface{}{"stations": out, "enabled": true, "updated_at": c.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z")})
}
