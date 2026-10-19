// Package web serves a small browser UI: satellite passes, a live S-meter
// and audio level, and the latest captures, on top of a JSON API.
package web

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/Chaim12345/sdrstream/go/internal/catalogue"
	"github.com/Chaim12345/sdrstream/go/internal/satellites"
	"github.com/Chaim12345/sdrstream/go/internal/satnogs"
	"github.com/Chaim12345/sdrstream/go/internal/tle"
	"github.com/Chaim12345/sdrstream/go/internal/tracking"
)

// Capture is one recording the UI can show.
type Capture struct {
	Satellite string    `json:"satellite"`
	Receiver  string    `json:"receiver"`
	Start     time.Time `json:"start"`
	Samples   int       `json:"samples"`
	Rate      int       `json:"rate"`
	Path      string    `json:"path"`
}

// Status is the live snapshot the UI polls.
type Status struct {
	UpdatedAt time.Time         `json:"updated_at"`
	Captures  []Capture         `json:"captures"`
	Passes    []PassInfo        `json:"passes"`
	Satellites []SatelliteInfo  `json:"satellites"`
	Receivers  int               `json:"receivers"`
}

// PassInfo is one predicted pass.
type PassInfo struct {
	Satellite    string    `json:"satellite"`
	NoradID      int       `json:"norad_id"`
	Receiver     string    `json:"receiver"`
	Rise         time.Time `json:"rise"`
	Peak         time.Time `json:"peak"`
	Set          time.Time `json:"set"`
	MaxElevation float64   `json:"max_elevation"`
}

// SatelliteInfo describes one satellite in the table.
type SatelliteInfo struct {
	Name       string `json:"name"`
	NoradID    int    `json:"norad_id"`
	DownlinkHz int64  `json:"downlink_hz"`
	Mode       string `json:"mode"`
	Band       string `json:"band"`
	Status     string `json:"status"`
	Receivers  int    `json:"receivers"`
}

// Server holds the state and serves the UI and API.
type Server struct {
	mu       sync.Mutex
	captures []Capture
	passes   []PassInfo
	html     string
}

// New builds a server with the bundled page.
func New() *Server {
	s := &Server{}
	s.loadPasses()
	return s
}

// loadPasses predicts passes for the next 12 hours over a few good receivers
// once at startup, so the UI has something to show without waiting for a sweep.
func (s *Server) loadPasses() {
	text, err := tle.Load("amateur", 24, false, 60*time.Second)
	if err != nil {
		log.Printf("web: tle load: %v", err)
		return
	}
	for _, sat := range satellites.Table {
		if sat.Status != "active" {
			continue
		}
		receivers := catalogue.ForFrequency(float64(sat.DownlinkHz))
		if len(receivers) == 0 {
			continue
		}
		tracker, err := tracking.FromTLEText(text, sat.Name)
		if err != nil {
			continue
		}
		for _, receiver := range receivers[:2] {
			passes, err := tracker.FindPasses(receiver.Latitude, receiver.Longitude,
				time.Now(), 12, 10)
			if err != nil {
				continue
			}
			for _, pass := range passes {
				s.passes = append(s.passes, PassInfo{
					Satellite: sat.Name, NoradID: sat.NoradID, Receiver: receiver.URL,
					Rise: pass.Rise, Peak: pass.Peak.Time, Set: pass.Set,
					MaxElevation: pass.MaxElevation(),
				})
			}
			break
		}
	}
	sort.Slice(s.passes, func(i, j int) bool { return s.passes[i].Rise.Before(s.passes[j].Rise) })
}

// AddCapture records a completed capture for the UI.
func (s *Server) AddCapture(capture Capture) {
	s.mu.Lock()
	s.captures = append(s.captures, capture)
	s.mu.Unlock()
}

// Handler returns the HTTP routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/status.json", s.handleStatus)
	mux.HandleFunc("/api/captures.json", s.handleCaptures)
	mux.HandleFunc("/api/satellites.json", s.handleSatellites)
	mux.HandleFunc("/api/observations.json", s.handleObservations)
	return mux
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, indexPage)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	captures := append([]Capture{}, s.captures...)
	s.mu.Unlock()
	writeJSON(w, Status{
		UpdatedAt: time.Now(), Captures: captures,
		Passes: s.passes, Receivers: len(catalogue.Table),
	})
}

func (s *Server) handleCaptures(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	captures := append([]Capture{}, s.captures...)
	s.mu.Unlock()
	writeJSON(w, captures)
}

func (s *Server) handleSatellites(w http.ResponseWriter, r *http.Request) {
	var out []SatelliteInfo
	for _, sat := range satellites.Table {
		out = append(out, SatelliteInfo{
			Name: sat.Name, NoradID: sat.NoradID, DownlinkHz: sat.DownlinkHz,
			Mode: sat.Mode, Status: sat.Status,
			Receivers: len(catalogue.ForFrequency(float64(sat.DownlinkHz))),
		})
	}
	writeJSON(w, out)
}

func (s *Server) handleObservations(w http.ResponseWriter, r *http.Request) {
	norad := r.URL.Query().Get("norad")
	if norad == "" {
		http.Error(w, "norad query parameter required", http.StatusBadRequest)
		return
	}
	var id int
	fmt.Sscanf(norad, "%d", &id)
	observations, err := satnogs.Observations(id, 15, 30*time.Second)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, observations)
}

func writeJSON(w http.ResponseWriter, value interface{}) {
	w.Header().Set("Content-Type", "application/json")
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		log.Printf("web: encode: %v", err)
	}
}

// Serve binds addr and serves the UI until the process exits.
func (s *Server) Serve(addr string) error {
	log.Printf("web UI on http://%s", addr)
	server := &http.Server{Addr: addr, Handler: s.Handler()}
	return server.ListenAndServe()
}
