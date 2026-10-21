// Package web serves a small browser UI: satellite passes, a live S-meter
// and audio level, and the latest captures, on top of a JSON API.
package web

import (
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
	"github.com/chaim12345/sdrstream/go/internal/gallery"
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
	Relay      string `json:"relay,omitempty"`
	Band       string `json:"band"`
	Status     string `json:"status"`
	Receivers  int    `json:"receivers"`
}

// Server holds the state and serves the UI and API.
type Server struct {
	mu           sync.Mutex
	captures     []Capture
	passes       []PassInfo
	gallery      []gallery.Item
	html         string
	capturesDir  string
	galleryDir   string
	galleryAt    time.Time
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
	end := time.Now().UTC().Add(6 * time.Hour)
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

// Handler returns the HTTP routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/status.json", s.handleStatus)
	mux.HandleFunc("/api/captures.json", s.handleCaptures)
	mux.HandleFunc("/api/satellites.json", s.handleSatellites)
	mux.HandleFunc("/api/observations.json", s.handleObservations)
	mux.HandleFunc("/api/gallery.json", s.handleGallery)
	mux.HandleFunc("/audio/", s.handleAudio)
	mux.HandleFunc("/gallery/", s.handleGalleryFile)
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
	scanned := s.scanCaptures()
	s.mu.Lock()
	captures := append(append([]Capture{}, scanned...), s.captures...)
	s.mu.Unlock()
	sort.Slice(captures, func(i, j int) bool { return captures[i].Start.Before(captures[j].Start) })
	writeJSON(w, Status{
		UpdatedAt: time.Now(), Captures: captures,
		Passes: s.passes, Receivers: len(catalogue.Table),
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
