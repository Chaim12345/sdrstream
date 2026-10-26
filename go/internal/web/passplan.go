package web

import "net/http"

// SetPassplan registers the multi-pass observation planner (read-only).
// Structural interface so web does not import passplan.
func (s *Server) SetPassplan(provider interface{ TimelineJSON() interface{} }) {
	s.mu.Lock()
	s.passplan = provider
	s.mu.Unlock()
}

// handlePassplan serves GET /api/passplan.json: the planned observations.
func (s *Server) handlePassplan(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	provider := s.passplan
	s.mu.Unlock()
	if provider == nil {
		writeJSON(w, map[string]interface{}{"enabled": false})
		return
	}
	out := map[string]interface{}{"enabled": true, "observations": provider.TimelineJSON()}
	// Optional: providers that report refresh status (last refresh, element
	// count, error, strategy, station) expose it as "info".
	if ip, ok := provider.(interface{ InfoJSON() interface{} }); ok {
		out["info"] = ip.InfoJSON()
	}
	writeJSON(w, out)
}
