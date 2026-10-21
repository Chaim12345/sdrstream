// Package web serves a small browser UI: satellite passes, a live S-meter
// and audio level, and the latest captures, on top of a JSON API.
package web

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/catalogue"
	"github.com/chaim12345/sdrstream/go/internal/directory"
	"github.com/chaim12345/sdrstream/go/internal/gallery"
	"github.com/chaim12345/sdrstream/go/internal/monitor"
	"github.com/chaim12345/sdrstream/go/internal/relays"
	"github.com/chaim12345/sdrstream/go/internal/satellites"
	"github.com/chaim12345/sdrstream/go/internal/satnogs"
	"github.com/chaim12345/sdrstream/go/internal/tle"
	"github.com/chaim12345/sdrstream/go/internal/tracking"
)

// Capture is one recording the UI can show.
type Capture struct {
	Satellite string    `json:"satellite"`
	Receiver  string    `json:"receiver"`
	Start     time.Time `json:"start"`
	Samples   int       `json:"samples"`
	Rate      int       `json:"rate"`
	Path      string    `json:"path"`
	URL       string    `json:"url,omitempty"`
}

// Status is the live snapshot the UI polls.
type Status struct {
	UpdatedAt  time.Time       `json:"updated_at"`
	Captures   []Capture       `json:"captures"`
	Passes     []PassInfo      `json:"passes"`
	Satellites []SatelliteInfo `json:"satellites"`
	Receivers  int             `json:"receivers"`
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
	Relay      string `json:"relay,omitempty"`
	Band       string `json:"band"`
	Status     string `json:"status"`
	Receivers  int    `json:"receivers"`
}

// Server holds the state and serves the UI and API.
type Server struct {
	mu          sync.Mutex
	captures    []Capture
	passes      []PassInfo
	gallery     []gallery.Item
	html        string
	capturesDir string
	galleryDir  string
	galleryAt   time.Time
	monitor     *monitor.Monitor
	stations    []directory.Station
	stationsAt  time.Time
	// fetchStations is directory.FetchStations in production and a seam
	// for the tests, which cannot wait on the public directory.
	fetchStations func(context.Context) ([]directory.Station, error)
}

// New builds a server with the bundled page. When capturesDir is non-empty
// the capture table is refreshed from WAV files found there on every poll,
// so recordings made by satrecord (a separate process) show up live. The
// gallery uses galleryDir (default: SatNOGS cache/gallery), first loading
// the manifest cached on disk and then refreshing it in the background.
func New(capturesDir, galleryDir string) *Server {
	if galleryDir == "" {
		galleryDir = filepath.Join(satnogs.CacheDir, "gallery")
	}
	s := &Server{capturesDir: capturesDir, galleryDir: galleryDir}
	s.loadGallery()
	go s.galleryLoop()
	s.loadPasses()
	go s.passesLoop()
	return s
}

// galleryLoop keeps the SatNOGS gallery fresh without blocking startup.
func (s *Server) galleryLoop() {
	s.refreshGallery()
	for range time.Tick(30 * time.Minute) {
		s.refreshGallery()
	}
}

// refreshGallery downloads new artefacts (images and text; raw binary is
// skipped) for observations from the last week and rewrites the manifest.
func (s *Server) refreshGallery() {
	// The window reaches two days past the local clock so that a clock
	// behind the network's does not hide the newest recordings.
	end := time.Now().UTC().AddDate(0, 0, 2)
	start := end.AddDate(0, 0, -7)
	observations, err := satnogs.Between(start, end, 0, 25, 60*time.Second)
	if err != nil {
		log.Printf("web: gallery observations: %v", err)
		return
	}
	items := gallery.Collect(observations)
	saved, err := gallery.Fetch(items, s.galleryDir, 120*time.Second, 20<<20, false)
	if err != nil {
		log.Printf("web: gallery fetch: %v", err)
	}
	if len(saved) == 0 {
		return
	}
	sort.SliceStable(saved, func(i, j int) bool { return saved[i].Start.After(saved[j].Start) })
	s.mu.Lock()
	s.gallery = saved
	s.galleryAt = time.Now()
	s.mu.Unlock()
	s.saveGallery()
}

// saveGallery writes the manifest next to the artefacts, with absolute
// paths stripped so the directory can be moved.
func (s *Server) saveGallery() {
	s.mu.Lock()
	items := make([]gallery.Item, len(s.gallery))
	copy(items, s.gallery)
	s.mu.Unlock()
	for i := range items {
		items[i].Path = ""
	}
	data, err := json.MarshalIndent(items, "", " ")
	if err != nil {
		return
	}
	_ = os.MkdirAll(s.galleryDir, 0o755)
	_ = os.WriteFile(filepath.Join(s.galleryDir, "index.json"), data, 0o644)
}

// loadGallery restores the manifest and drops entries whose files vanished.
func (s *Server) loadGallery() {
	data, err := os.ReadFile(filepath.Join(s.galleryDir, "index.json"))
	if err != nil {
		return
	}
	var items []gallery.Item
	if err := json.Unmarshal(data, &items); err != nil {
		return
	}
	kept := items[:0]
	for _, item := range items {
		path := filepath.Join(s.galleryDir, item.Name)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		item.Path = path
		kept = append(kept, item)
	}
	s.mu.Lock()
	s.gallery = kept
	s.mu.Unlock()
}

// loadPasses predicts passes for the next 12 hours over a few good receivers
// and swaps the table atomically, so a refresh never shows a half-built list.
func (s *Server) loadPasses() {
	text, err := tle.Load("amateur", 24, false, 60*time.Second)
	if err != nil {
		log.Printf("web: tle load: %v", err)
		return
	}
	var passes []PassInfo
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
			passesForReceiver, err := tracker.FindPasses(receiver.Latitude, receiver.Longitude,
				time.Now(), 12, 10)
			if err != nil {
				continue
			}
			for _, pass := range passesForReceiver {
				passes = append(passes, PassInfo{
					Satellite: sat.Name, NoradID: sat.NoradID, Receiver: receiver.URL,
					Rise: pass.Rise, Peak: pass.Peak.Time, Set: pass.Set,
					MaxElevation: pass.MaxElevation(),
				})
			}
			break
		}
	}
	sort.Slice(passes, func(i, j int) bool { return passes[i].Rise.Before(passes[j].Rise) })
	s.mu.Lock()
	s.passes = passes
	s.mu.Unlock()
}

// passesLoop refreshes the pass table while the server runs: a snapshot taken
// at startup is stale within the hour, and the UI calls the next 12 hours
// "upcoming".
func (s *Server) passesLoop() {
	for range time.Tick(30 * time.Minute) {
		s.loadPasses()
	}
}

// AddCapture records a completed capture for the UI.
func (s *Server) AddCapture(capture Capture) {
	s.mu.Lock()
	s.captures = append(s.captures, capture)
	s.mu.Unlock()
}

// scanCaptures lists every WAV in the captures directory as a Capture.
// The satellite name is the leading part of the filename ("CAS-9_...wav"),
// the sample rate comes from the WAV header, and the size gives the sample
// count (16-bit mono).
func (s *Server) scanCaptures() []Capture {
	if s.capturesDir == "" {
		return nil
	}
	entries, err := os.ReadDir(s.capturesDir)
	if err != nil {
		return nil
	}
	var out []Capture
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(name), ".wav") {
			continue
		}
		path := filepath.Join(s.capturesDir, name)
		info, err := entry.Info()
		if err != nil {
			continue
		}
		rate, dataBytes, err := wavInfo(path)
		if err != nil || dataBytes == 0 {
			continue
		}
		satellite := name
		if i := strings.IndexByte(name, '_'); i > 0 {
			satellite = name[:i]
		}
		out = append(out, Capture{
			Satellite: satellite,
			Receiver:  "sdrstream",
			Start:     info.ModTime().UTC(),
			Samples:   int(dataBytes / 2),
			Rate:      rate,
			Path:      name,
			URL:       "/audio/" + url.PathEscape(name),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out
}

// wavInfo reads the sample rate and data-chunk size from a RIFF/WAVE file.
func wavInfo(path string) (rate int, dataBytes int64, err error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer file.Close()
	header := make([]byte, 44)
	if _, err = io.ReadFull(file, header); err != nil {
		return 0, 0, err
	}
	if string(header[0:4]) != "RIFF" || string(header[8:12]) != "WAVE" {
		return 0, 0, os.ErrInvalid
	}
	rate = int(binary.LittleEndian.Uint32(header[24:28]))
	if rate <= 0 || rate > 1_000_000 {
		return 0, 0, os.ErrInvalid
	}
	if string(header[36:40]) == "data" {
		return rate, int64(binary.LittleEndian.Uint32(header[40:44])), nil
	}
	// Walk chunks until "data" (headers with extra LIST/INFO chunks).
	offset := int64(12)
	for {
		if _, err = file.Seek(offset, io.SeekStart); err != nil {
			return 0, 0, err
		}
		chunk := make([]byte, 8)
		if _, err = io.ReadFull(file, chunk); err != nil {
			return 0, 0, err
		}
		size := int64(binary.LittleEndian.Uint32(chunk[4:8]))
		offset += 8 + size + size%2
		if string(chunk[0:4]) == "data" {
			return rate, size, nil
		}
		if offset <= 12 || size == 0 {
			return 0, 0, os.ErrInvalid
		}
	}
}

// securityHeaders sets the headers every response carries. The UI is a
// single page with an inline script, so the policy allows inline code but
// blocks framing and cross-origin access.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("X-Frame-Options", "DENY")
		header.Set("Referrer-Policy", "no-referrer")
		header.Set("Content-Security-Policy",
			"default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; "+
				"img-src 'self' data:; media-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'none'")
		next.ServeHTTP(w, r)
	})
}

// Handler returns the HTTP routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/status.json", s.handleStatus)
	mux.HandleFunc("/api/captures.json", s.handleCaptures)
	mux.HandleFunc("/api/satellites.json", s.handleSatellites)
	mux.HandleFunc("/api/observations.json", s.handleObservations)
	mux.HandleFunc("/api/gallery.json", s.handleGallery)
	mux.HandleFunc("/api/activity.json", s.handleActivity)
	mux.HandleFunc("/api/spectrum.json", s.handleSpectrum)
	mux.HandleFunc("/api/stations.json", s.handleStations)
	mux.HandleFunc("/audio/", s.handleAudio)
	mux.HandleFunc("/gallery/", s.handleGalleryFile)
	return securityHeaders(mux)
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
	scanned := s.scanCaptures()
	s.mu.Lock()
	captures := append(append([]Capture{}, scanned...), s.captures...)
	passes := append([]PassInfo(nil), s.passes...)
	s.mu.Unlock()
	sort.Slice(captures, func(i, j int) bool { return captures[i].Start.Before(captures[j].Start) })
	writeJSON(w, Status{
		UpdatedAt: time.Now(), Captures: captures,
		Passes: passes, Receivers: len(catalogue.Table),
	})
}

func (s *Server) handleCaptures(w http.ResponseWriter, r *http.Request) {
	captures := s.scanCaptures()
	s.mu.Lock()
	captures = append(captures, s.captures...)
	s.mu.Unlock()
	sort.Slice(captures, func(i, j int) bool { return captures[i].Start.Before(captures[j].Start) })
	writeJSON(w, captures)
}

// handleAudio serves WAV files from the captures directory so the browser
// can play recordings; names are unescaped then joined inside the directory,
// and a path separator or traversal is refused.
func (s *Server) handleAudio(w http.ResponseWriter, r *http.Request) {
	if s.capturesDir == "" {
		http.Error(w, "captures directory not configured", http.StatusNotFound)
		return
	}
	name, err := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/audio/"))
	if err != nil || strings.ContainsAny(name, "/\\") || name == "" {
		http.NotFound(w, r)
		return
	}
	// Only recordings are served; the directory may hold other files.
	switch strings.ToLower(filepath.Ext(name)) {
	case ".wav", ".mp3", ".ogg", ".flac":
	default:
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, filepath.Join(s.capturesDir, name))
}

// handleGallery lists the saved artefacts. URLs are rewritten to local
// files so the browser never depends on the SatNOGS S3 bucket.
func (s *Server) handleGallery(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	items := make([]gallery.Item, len(s.gallery))
	copy(items, s.gallery)
	s.mu.Unlock()
	for i := range items {
		items[i].URL = "/gallery/" + url.PathEscape(items[i].Name)
		items[i].Path = ""
	}
	writeJSON(w, items)
}

// handleGalleryFile serves one downloaded artefact, but only names that are
// present in the manifest, so the directory cannot be browsed or escaped.
func (s *Server) handleGalleryFile(w http.ResponseWriter, r *http.Request) {
	name, err := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/gallery/"))
	if err != nil || name == "" || strings.ContainsAny(name, "/\\") {
		http.NotFound(w, r)
		return
	}
	s.mu.Lock()
	found := false
	for _, item := range s.gallery {
		if item.Name == name {
			found = true
			break
		}
	}
	s.mu.Unlock()
	if !found {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, filepath.Join(s.galleryDir, name))
}

// SetMonitor attaches a live monitor to the server.
func (s *Server) SetMonitor(instance *monitor.Monitor) {
	s.mu.Lock()
	s.monitor = instance
	s.mu.Unlock()
}

// handleActivity returns the transmissions the monitor has seen.
func (s *Server) handleActivity(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	instance := s.monitor
	s.mu.Unlock()
	if instance == nil {
		writeJSON(w, []monitor.Activity{})
		return
	}
	writeJSON(w, instance.Activities())
}

// handleSpectrum returns the latest spectrum frame from the monitored
// receiver, downsampled to a size the browser can draw.
func (s *Server) handleSpectrum(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	instance := s.monitor
	s.mu.Unlock()
	if instance == nil {
		http.Error(w, "no monitor is running", http.StatusNotFound)
		return
	}
	bins, centre, span, ok := instance.Spectrum()
	if !ok || len(bins) == 0 {
		// A receiver that sends no spectrum (WebSDR, KiwiSDR) or a
		// monitor that has not seen a frame yet is not an error: the
		// page asks for this every second and would log a failure.
		writeJSON(w, map[string]interface{}{
			"available":  false,
			"bins":       []float64{},
			"centre_hz":  centre,
			"span_hz":    span,
			"updated_at": time.Now(),
		})
		return
	}
	// Keep the strongest bin of every group: a narrow transmission is
	// usually one or two bins wide and decimation would drop it.
	const points = 256
	step := len(bins) / points
	if step < 1 {
		step = 1
	}
	var out []float64
	for i := 0; i < len(bins); i += step {
		highest := bins[i]
		for j := i + 1; j < i+step && j < len(bins); j++ {
			if bins[j] > highest {
				highest = bins[j]
			}
		}
		out = append(out, highest)
	}
	writeJSON(w, map[string]interface{}{
		"available":  true,
		"bins":       out,
		"centre_hz":  centre,
		"span_hz":    span,
		"updated_at": time.Now(),
	})
}

// handleStations lists the public receivers from the directory. The list is
// cached for an hour because the directory page is large. A failed refresh
// with a cache to fall back on serves the cache and marks it stale; a failed
// refresh without a cache is a 502, so the page can tell "nothing found"
// apart from "the directory is unreachable".
func (s *Server) handleStations(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	stations := s.stations
	fresh := !s.stationsAt.IsZero() && time.Since(s.stationsAt) < time.Hour
	fetch := s.fetchStations
	s.mu.Unlock()
	if fetch == nil {
		fetch = func(ctx context.Context) ([]directory.Station, error) {
			return directory.FetchStations(ctx, 30*time.Second)
		}
	}
	if !fresh {
		fetched, err := fetch(r.Context())
		if err == nil {
			s.mu.Lock()
			s.stations = fetched
			s.stationsAt = time.Now()
			stations = fetched
			s.mu.Unlock()
		} else {
			log.Printf("web: station directory: %v", err)
			if stations == nil {
				http.Error(w, "station directory unavailable: "+err.Error(), http.StatusBadGateway)
				return
			}
			w.Header().Set("X-Stations-Stale", "true")
		}
	}
	if stations == nil {
		stations = []directory.Station{}
	}
	writeJSON(w, stations)
}

func (s *Server) handleSatellites(w http.ResponseWriter, r *http.Request) {
	var out []SatelliteInfo
	for _, sat := range satellites.Table {
		relay := ""
		if relays.IsRelay(sat) && sat.Status == "active" {
			relay = string(relays.KindOf(sat))
		}
		out = append(out, SatelliteInfo{
			Name: sat.Name, NoradID: sat.NoradID, DownlinkHz: sat.DownlinkHz,
			Mode: sat.Mode, Relay: relay, Status: sat.Status,
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
