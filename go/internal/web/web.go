// Package web serves a small browser UI: satellite passes, a live S-meter
// and audio level, and the latest captures, on top of a JSON API.
package web

import (
	"context"
	"embed"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/bandcond"
	"github.com/chaim12345/sdrstream/go/internal/catalogue"
	"github.com/chaim12345/sdrstream/go/internal/directory"
	"github.com/chaim12345/sdrstream/go/internal/feeds"
	"github.com/chaim12345/sdrstream/go/internal/gallery"
	"github.com/chaim12345/sdrstream/go/internal/monitor"
	"github.com/chaim12345/sdrstream/go/internal/relays"
	"github.com/chaim12345/sdrstream/go/internal/satellites"
	"github.com/chaim12345/sdrstream/go/internal/satnogs"
	"github.com/chaim12345/sdrstream/go/internal/sources"
	"github.com/chaim12345/sdrstream/go/internal/tle"
	"github.com/chaim12345/sdrstream/go/internal/tracking"
	"github.com/chaim12345/sdrstream/go/internal/verify"
)

// Capture is one recording the UI can show.
type Capture struct {
	Satellite   string    `json:"satellite"`
	Receiver    string    `json:"receiver"`
	FrequencyHz float64   `json:"frequency_hz,omitempty"`
	Mode        string    `json:"mode,omitempty"`
	Decode      string    `json:"decode,omitempty"`
	Start       time.Time `json:"start"`
	Samples     int       `json:"samples"`
	Rate        int       `json:"rate"`
	Path        string    `json:"path"`
	URL         string    `json:"url,omitempty"`
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
type LiveGalleryItem struct {
	ObservationID int       `json:"observation_id"`
	Satellite     string    `json:"satellite"`
	Station       string    `json:"station"`
	Start         time.Time `json:"start"`
	Mode          string    `json:"mode"`
	FrequencyHz   int64     `json:"frequency_hz"`
	Kind          string    `json:"kind"`
	Name          string    `json:"name"`
	URL           string    `json:"url"`
	AudioURL      string    `json:"audio_url,omitempty"`
	Size          int64     `json:"size,omitempty"`
	Text          string    `json:"text,omitempty"`
}

type ScanStatus struct {
	StationType  string     `json:"station_type,omitempty"`
	Station      string     `json:"station"`
	FrequencyHz  float64    `json:"frequency_hz"`
	Mode         string     `json:"mode"`
	State        string     `json:"state"`
	Result       string     `json:"result,omitempty"`
	BlockedUntil *time.Time `json:"blocked_until,omitempty"`
	UpdatedAt    time.Time  `json:"updated_at"`
	// BandCondition is the advisory HF band label (from the cached NOAA
	// K-index) for scan targets inside an amateur HF band.
	BandCondition string `json:"band_condition,omitempty"`
}

// ScanMetrics reports the bounded station-scan ramp and its current load.
type DroppedStation struct {
	Station string     `json:"station"`
	Reason  string     `json:"reason"`
	Until   *time.Time `json:"until,omitempty"`
}

type ScanMetrics struct {
	RampStep               int              `json:"ramp_step"`
	MaxConnections         int              `json:"max_connections"`
	ActiveConnections      int              `json:"active_connections"`
	Refusals               int              `json:"refusals"`
	Timeouts               int              `json:"timeouts"`
	LoadAverage            float64          `json:"load_average"`
	CoreCount              int              `json:"core_count"`
	EstimatedBandwidthMbps float64          `json:"estimated_bandwidth_mbps"`
	BandwidthLimitMbps     float64          `json:"bandwidth_limit_mbps"`
	TargetConcurrency      int              `json:"target_concurrency"`
	Paused                 bool             `json:"paused"`
	MemoryBytes            uint64           `json:"memory_bytes"`
	OpenFDs                int              `json:"open_fds"`
	Stations               []ScanStatus     `json:"stations"`
	Dropped                []DroppedStation `json:"dropped"`
}

type Server struct {
	mu                sync.Mutex
	captures          []Capture
	passes            []PassInfo
	gallery           []gallery.Item
	liveGallery       []LiveGalleryItem
	html              string
	capturesDir       string
	galleryDir        string
	galleryAt         time.Time
	monitor           *monitor.Monitor
	feeds             *feeds.Manager
	scans             []ScanStatus
	scanIndex         map[string]int
	scanMetrics       ScanMetrics
	scanLog           []ScanStatus
	scanLogSeq        uint64
	scanControlManual bool
	stations          []directory.Station
	stationsAt        time.Time
	sourcesResults    []sources.Result
	sourcesAt         time.Time
	sourcesRunning    bool
	cond              bandcond.Conditions
	condAt            time.Time
	condErr           string
	// rdioHook, when set, receives every verified live artifact that has
	// audio, so the fleet scan can feed an Rdio Scanner archive.
	rdioHook func(monitor.Activity)
	// healthAll and fetchConditions are seams for the tests.
	healthAll       func(ctx context.Context, opts sources.Options) []sources.Result
	fetchConditions func(ctx context.Context) (bandcond.Conditions, error)
	// fetchStations is directory.FetchStations in production and a seam
	// for the tests, which cannot wait on the public directory.
	fetchStations func(context.Context) ([]directory.Station, error)
	NoAuth        bool
	auth          authState
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
	s := &Server{capturesDir: capturesDir, galleryDir: galleryDir, scanIndex: make(map[string]int)}
	s.loadScanState()
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

// AddCaptureMetadata persists scanner metadata alongside the WAV filename.
func (s *Server) AddCaptureMetadata(name, mode, receiver, detail string, frequencyHz float64) {
	if s.capturesDir == "" || filepath.Base(name) != name {
		return
	}
	_ = os.WriteFile(filepath.Join(s.capturesDir, strings.TrimSuffix(name, ".wav")+".txt"), []byte(fmt.Sprintf("frequency_hz=%.0f\nmode=%s\nreceiver=%s\ndecode=%s\n", frequencyHz, mode, receiver, strings.ReplaceAll(detail, "\n", " "))), 0644)
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
		parts := strings.Split(strings.TrimSuffix(name, ".wav"), "_")
		var freq float64
		mode, receiver, detail := "", "sdrstream", ""
		for _, part := range parts {
			if strings.HasSuffix(part, "kHz") {
				v, _ := strconv.ParseFloat(strings.TrimSuffix(part, "kHz"), 64)
				freq = v * 1000
			}
			if part == "aprs" || part == "pocsag" || part == "voice" || part == "ft8" || part == "wspr" {
				mode = part
			}
			if strings.HasPrefix(part, "jukebox") {
				receiver = "jukebox.spdns.eu:6166"
			}
		}
		if sidecar, err := os.ReadFile(strings.TrimSuffix(path, ".wav") + ".txt"); err == nil {
			for _, line := range strings.Split(string(sidecar), "\n") {
				if strings.HasPrefix(line, "frequency_hz=") {
					freq, _ = strconv.ParseFloat(strings.TrimPrefix(line, "frequency_hz="), 64)
				}
				if strings.HasPrefix(line, "mode=") {
					mode = strings.TrimPrefix(line, "mode=")
				}
				if strings.HasPrefix(line, "receiver=") {
					receiver = strings.TrimPrefix(line, "receiver=")
				}
				if strings.HasPrefix(line, "decode=") {
					detail = strings.TrimPrefix(line, "decode=")
				}
			}
		}
		out = append(out, Capture{
			Satellite:   satellite,
			Receiver:    receiver,
			FrequencyHz: freq, Mode: mode, Decode: detail,
			Start:   info.ModTime().UTC(),
			Samples: int(dataBytes / 2),
			Rate:    rate,
			Path:    name,
			URL:     "/audio/" + url.PathEscape(name),
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

// securityHeaders sets headers for the single-page UI and same-origin assets.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("X-Frame-Options", "DENY")
		header.Set("Referrer-Policy", "no-referrer")
		header.Set("Content-Security-Policy", "default-src 'none'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; media-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

//go:embed static
var staticFS embed.FS

// Handler returns the HTTP routes.
func (s *Server) Handler() http.Handler {
	s.auth.init()
	mux := http.NewServeMux()
	mux.HandleFunc("/login", s.handleLogin)
	mux.HandleFunc("/logout", s.handleLogout)
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/status.json", s.handleStatus)
	mux.HandleFunc("/api/captures.json", s.handleCaptures)
	mux.HandleFunc("/api/satellites.json", s.handleSatellites)
	mux.HandleFunc("/api/observations.json", s.handleObservations)
	mux.HandleFunc("/api/gallery.json", s.handleGallery)
	mux.HandleFunc("/api/test-fixtures.json", s.handleTestFixtures)
	mux.HandleFunc("/api/scans.json", s.handleScans)
	mux.HandleFunc("/api/feeds.json", s.handleFeeds)
	mux.HandleFunc("/api/sources.json", s.handleSources)
	mux.HandleFunc("/api/bandconditions.json", s.handleBandConditions)
	mux.HandleFunc("/api/scanlog.json", s.handleScanLog)
	mux.HandleFunc("/api/scans/control", s.handleScanControl)
	mux.HandleFunc("/api/monitor/control", s.handleMonitorControl)
	mux.HandleFunc("/api/activity.json", s.handleActivity)
	mux.HandleFunc("/api/spectrum.json", s.handleSpectrum)
	mux.HandleFunc("/api/live.json", s.handleLive)
	mux.HandleFunc("/api/tune.json", s.handleTune)
	mux.HandleFunc("/api/stations.json", s.handleStations)
	mux.HandleFunc("/api/map.json", s.handleMap)
	mux.HandleFunc("/api/tiles/", s.handleTile)
	mux.Handle("/static/", http.FileServerFS(staticFS))
	mux.HandleFunc("/audio/", s.handleAudio)
	mux.HandleFunc("/audio/live/", s.handleLiveAudio)
	mux.HandleFunc("/gallery/", s.handleGalleryFile)
	return securityHeaders(s.wrapAuth(mux))
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
	items := append([]LiveGalleryItem(nil), s.liveGallery...)
	sat := append([]gallery.Item(nil), s.gallery...)
	s.mu.Unlock()
	for i := range sat {
		sat[i].URL = "/gallery/" + url.PathEscape(sat[i].Name)
		sat[i].Path = ""
	}
	out := make([]interface{}, 0, len(items)+len(sat))
	for _, item := range items {
		out = append(out, item)
	}
	for _, item := range sat {
		out = append(out, item)
	}
	writeJSON(w, out)
}

// AddLiveArtifact saves a verified decoder result with source metadata.
func (s *Server) AddLiveArtifact(activity monitor.Activity, pngData []byte) error {
	if err := verify.Live(activity.Kind, activity.Mode, activity.Detail, pngData, activity.VerificationAnchor); err != nil {
		return fmt.Errorf("live verification rejected content: %w", err)
	}
	name := fmt.Sprintf("live-%s-%d", activity.Started.UTC().Format("20060102T150405Z"), activity.ID)
	var ext, kind string
	var data []byte
	if len(pngData) > 0 {
		ext, kind, data = ".png", "image", pngData
	} else {
		ext, kind, data = ".txt", "text", []byte(activity.Detail)
	}
	name += ext
	if err := os.MkdirAll(s.galleryDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(s.galleryDir, name), data, 0o644); err != nil {
		return err
	}
	station := activity.Label
	if station == "" {
		station = activity.Source
	}
	item := LiveGalleryItem{ObservationID: 0, Satellite: "Live reception", Station: station, Start: activity.Started, Mode: activity.Mode, FrequencyHz: int64(activity.FrequencyHz), Kind: kind, Name: name, URL: "/gallery/" + url.PathEscape(name), Size: int64(len(data)), Text: activity.Detail}
	if activity.AudioPath != "" {
		audioName := name + ".wav"
		src, err := os.ReadFile(activity.AudioPath)
		if err == nil && len(src) > 0 {
			if err := os.WriteFile(filepath.Join(s.galleryDir, audioName), src, 0o644); err == nil {
				item.AudioURL = "/audio/live/" + url.PathEscape(audioName)
			}
		}
	}
	s.mu.Lock()
	s.liveGallery = append([]LiveGalleryItem{item}, s.liveGallery...)
	hook := s.rdioHook
	s.mu.Unlock()
	if hook != nil && activity.AudioPath != "" {
		hook(activity)
	}
	return nil
}

// SetRdioHook installs the sink for verified live artifacts that carry
// audio. The hook runs after the gallery write, outside the server lock.
func (s *Server) SetRdioHook(hook func(monitor.Activity)) {
	s.mu.Lock()
	s.rdioHook = hook
	s.mu.Unlock()
}

func (s *Server) handleLiveAudio(w http.ResponseWriter, r *http.Request) {
	name, err := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/audio/live/"))
	if err != nil || name == "" || strings.ContainsAny(name, "/\\") {
		http.NotFound(w, r)
		return
	}
	s.mu.Lock()
	found := false
	for _, item := range s.liveGallery {
		if item.AudioURL != "" && strings.HasSuffix(item.AudioURL, url.PathEscape(name)) {
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
	for _, item := range s.liveGallery {
		if item.Name == name {
			found = true
			break
		}
	}
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

// SetFeeds attaches live public feeds to the server.
func (s *Server) SetFeeds(m *feeds.Manager) { s.mu.Lock(); s.feeds = m; s.mu.Unlock() }

func (s *Server) handleFeeds(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	m := s.feeds
	s.mu.Unlock()
	if m == nil {
		writeJSON(w, feeds.Snapshot{UpdatedAt: time.Now(), Sources: []feeds.SourceStatus{}, Items: []feeds.Item{}})
		return
	}
	writeJSON(w, m.Snapshot(500))
}

func scanStateFile() string { return stateFile("sdrstream-scan-state.json") }
func (s *Server) loadScanState() {
	b, err := os.ReadFile(scanStateFile())
	if err != nil {
		return
	}
	var items []ScanStatus
	if json.Unmarshal(b, &items) != nil {
		return
	}
	for _, item := range items {
		s.UpsertScanStatus(item)
	}
}
func (s *Server) persistScanStateLocked() {
	b, err := json.Marshal(s.scans)
	if err != nil {
		return
	}
	writeStateFile(scanStateFile(), b)
}

// SetMonitor attaches a live monitor to the server.
func (s *Server) SetScanStatuses(scans []ScanStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old := make(map[string]ScanStatus, len(s.scans))
	for _, item := range s.scans {
		old[scanLogKey(item)] = item
	}
	if len(scans) > 500 {
		scans = scans[len(scans)-500:]
	}
	s.scans = append([]ScanStatus(nil), scans...)
	for _, item := range scans {
		key := scanLogKey(item)
		prev, ok := old[key]
		if !ok || prev.State != item.State || prev.Result != item.Result {
			s.scanLogSeq++
			s.scanLog = append(s.scanLog, item)
			if len(s.scanLog) > 2000 {
				s.scanLog = append([]ScanStatus(nil), s.scanLog[len(s.scanLog)-2000:]...)
			}
		}
	}
	s.persistScanStateLocked()
}

func (s *Server) UpsertScanStatus(item ScanStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.scanIndex == nil {
		s.scanIndex = make(map[string]int, len(s.scans))
		for i, x := range s.scans {
			s.scanIndex[scanLogKey(x)] = i
		}
	}
	key := scanLogKey(item)
	idx, ok := s.scanIndex[key]
	if ok && idx < len(s.scans) {
		prev := s.scans[idx]
		s.scans[idx] = item
		if prev.State != item.State || prev.Result != item.Result {
			s.scanLogSeq++
			s.scanLog = append(s.scanLog, item)
			if len(s.scanLog) > 2000 {
				s.scanLog = append([]ScanStatus(nil), s.scanLog[len(s.scanLog)-2000:]...)
			}
		}
		s.persistScanStateLocked()
		return
	}
	if len(s.scans) >= 500 {
		delete(s.scanIndex, scanLogKey(s.scans[0]))
		s.scans = append(s.scans[1:], item)
		for i, x := range s.scans {
			s.scanIndex[scanLogKey(x)] = i
		}
	} else {
		s.scanIndex[key] = len(s.scans)
		s.scans = append(s.scans, item)
	}
	s.scanLogSeq++
	s.scanLog = append(s.scanLog, item)
	if len(s.scanLog) > 2000 {
		s.scanLog = append([]ScanStatus(nil), s.scanLog[len(s.scanLog)-2000:]...)
	}
	s.persistScanStateLocked()
}

func scanLogKey(item ScanStatus) string {
	return fmt.Sprintf("%s|%.0f|%s", item.Station, item.FrequencyHz, item.Mode)
}

func (s *Server) handleScanLog(w http.ResponseWriter, r *http.Request) {
	var since uint64
	if raw := r.URL.Query().Get("since"); raw != "" {
		since, _ = strconv.ParseUint(raw, 10, 64)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	start := uint64(0)
	if uint64(len(s.scanLog)) > s.scanLogSeq {
		start = 0
	} else {
		start = s.scanLogSeq - uint64(len(s.scanLog))
	}
	items := make([]map[string]any, 0)
	for i, item := range s.scanLog {
		seq := start + uint64(i) + 1
		if seq > since {
			items = append(items, map[string]any{"id": seq, "station": item.Station, "station_type": item.StationType, "frequency_hz": item.FrequencyHz, "mode": item.Mode, "state": item.State, "reason": item.Result, "updated_at": item.UpdatedAt})
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return fmt.Sprint(items[i]["updated_at"]) < fmt.Sprint(items[j]["updated_at"]) })
	writeJSON(w, map[string]any{"items": items, "next_since": s.scanLogSeq})
}

func (s *Server) handleScans(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	metrics := s.scanMetrics
	if metrics.TargetConcurrency == 0 {
		metrics.TargetConcurrency = metrics.RampStep
	}
	metrics.Stations = make([]ScanStatus, 0, len(s.scans))
	metrics.Dropped = make([]DroppedStation, 0)
	for _, item := range s.scans {
		if item.State == "blocked" || strings.Contains(strings.ToLower(item.State), "error") || strings.Contains(strings.ToLower(item.State), "timeout") || strings.Contains(strings.ToLower(item.State), "refused") || strings.Contains(strings.ToLower(item.State), "failed") {
			metrics.Dropped = append(metrics.Dropped, DroppedStation{Station: item.Station, Reason: item.Result, Until: item.BlockedUntil})
			continue
		}
		metrics.Stations = append(metrics.Stations, item)
	}
	if len(s.cond.Bands) > 0 && time.Since(s.condAt) < 3*time.Hour {
		for i := range metrics.Stations {
			if label, ok := s.cond.LabelForHz(metrics.Stations[i].FrequencyHz); ok {
				band, _ := bandcond.BandForHz(metrics.Stations[i].FrequencyHz)
				metrics.Stations[i].BandCondition = band + " " + label
			}
		}
	}
	writeJSON(w, metrics)
}

// SetScanMetrics publishes live ramp and resource counters for the scanner.
func (s *Server) handleScanControl(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get("Origin") != "https://"+r.Host && r.Header.Get("Origin") != "http://"+r.Host {
		http.Error(w, "same-origin required", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	if raw := r.FormValue("concurrency"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 100 {
			s.mu.Unlock()
			http.Error(w, "concurrency must be 1..100", http.StatusBadRequest)
			return
		}
		s.scanMetrics.TargetConcurrency = n
		s.scanControlManual = true
	}
	if raw := r.FormValue("paused"); raw != "" {
		s.scanMetrics.Paused = raw == "true"
	}
	metrics := s.scanMetrics
	s.mu.Unlock()
	writeJSON(w, map[string]any{"target_concurrency": metrics.TargetConcurrency, "paused": metrics.Paused})
}

// monitorModes and monitorSteps are the only values the control endpoint
// accepts: anything else is a client bug or an attack, not a mode.
var monitorModes = map[string]bool{
	"auto": true, "nfm": true, "fm": true, "wfm": true,
	"am": true, "usb": true, "lsb": true, "cw": true,
}
var monitorSteps = map[float64]bool{
	1000: true, 2500: true, 5000: true, 10000: true,
	12500: true, 25000: true, 50000: true, 100000: true,
}

// handleMonitorControl applies the live receiver knobs (squelch threshold,
// dial step, listening mode) from the Receiver panel. Same-origin POST only,
// like the scan control; the monitor picks the values up on its next pass.
func (s *Server) handleMonitorControl(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get("Origin") != "https://"+r.Host && r.Header.Get("Origin") != "http://"+r.Host {
		http.Error(w, "same-origin required", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	instance := s.monitor
	s.mu.Unlock()
	if instance == nil {
		http.Error(w, "no monitor is running", http.StatusNotFound)
		return
	}
	var controls monitor.Controls
	if raw := r.FormValue("threshold_db"); raw != "" {
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil || v < 1 || v > 60 {
			http.Error(w, "threshold_db must be 1..60", http.StatusBadRequest)
			return
		}
		controls.ThresholdDB = v
	}
	if raw := r.FormValue("step_hz"); raw != "" {
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil || !monitorSteps[v] {
			http.Error(w, "step_hz must be one of the listed steps", http.StatusBadRequest)
			return
		}
		controls.StepHz = v
	}
	if raw := r.FormValue("mode"); raw != "" {
		if !monitorModes[raw] {
			http.Error(w, "unknown mode", http.StatusBadRequest)
			return
		}
		controls.Mode = raw
	}
	instance.SetControls(controls)
	info := instance.Live()
	writeJSON(w, map[string]interface{}{
		"threshold_db": info.ThresholdDB,
		"step_hz":      info.StepHz,
		"mode":         info.Mode,
	})
}

func (s *Server) ScanControl() (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	target := s.scanMetrics.TargetConcurrency
	if target < 1 {
		target = s.scanMetrics.RampStep
	}
	if target < 1 {
		target = 1
	}
	if target > s.scanMetrics.MaxConnections && s.scanMetrics.MaxConnections > 0 {
		target = s.scanMetrics.MaxConnections
	}
	return target, s.scanMetrics.Paused
}

func (s *Server) ScanControlManual() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.scanControlManual
}

func (s *Server) SetScanMetrics(metrics ScanMetrics) {
	s.mu.Lock()
	target, paused := s.scanMetrics.TargetConcurrency, s.scanMetrics.Paused
	if !s.scanControlManual || target == 0 {
		target = metrics.TargetConcurrency
	}
	metrics.TargetConcurrency, metrics.Paused = target, paused
	metrics.Stations = append([]ScanStatus(nil), s.scans...)
	s.scanMetrics = metrics
	s.mu.Unlock()
}

func (s *Server) SetMonitor(instance *monitor.Monitor) {
	s.mu.Lock()
	s.monitor = instance
	s.mu.Unlock()
}

// fixtureDir locates test fixtures independent of the process working directory.
func fixtureDir() string {
	isDir := func(path string) bool { st, err := os.Stat(path); return err == nil && st.IsDir() }
	if dir := os.Getenv("SDRSTREAM_FIXTURE_DIR"); dir != "" && isDir(dir) {
		return dir
	}
	if _, file, _, ok := runtime.Caller(0); ok {
		dir := filepath.Join(filepath.Dir(file), "..", "..", "..", "tests", "data")
		if isDir(dir) {
			return dir
		}
	}
	if isDir("tests/data") {
		return "tests/data"
	}
	return ""
}

// handleTestFixtures lists local test WAV fixtures separately from live reception.
func (s *Server) handleTestFixtures(w http.ResponseWriter, r *http.Request) {
	type Fixture struct {
		Name string `json:"name"`
		Kind string `json:"kind"`
		Size int64  `json:"size"`
	}
	root := fixtureDir()
	if root == "" {
		writeJSON(w, []Fixture{})
		return
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		writeJSON(w, []Fixture{})
		return
	}
	var fixtures []Fixture
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".wav") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		fixtures = append(fixtures, Fixture{Name: entry.Name(), Kind: "test audio", Size: info.Size()})
	}
	writeJSON(w, fixtures)
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

// handleLive returns the monitor's point-in-time view: receiver identity,
// noise floor, peak level and the on-air peaks above the threshold. The
// live UI polls it every second; available:false means no monitor runs.
func (s *Server) handleLive(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	instance := s.monitor
	s.mu.Unlock()
	if instance == nil {
		writeJSON(w, monitor.LiveInfo{Available: false, OnAir: []monitor.OnAirPeak{}, UpdatedAt: time.Now()})
		return
	}
	info := instance.Live()
	info.Available = true
	writeJSON(w, info)
}

// handleTune parks the monitor on a frequency the user clicked in the
// waterfall and queues an immediate listen there (click-to-tune).
func (s *Server) handleTune(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get("Origin") != "https://"+r.Host && r.Header.Get("Origin") != "http://"+r.Host {
		http.Error(w, "same-origin required", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	hz, err := strconv.ParseFloat(r.FormValue("frequency_hz"), 64)
	if err != nil || hz <= 0 {
		http.Error(w, "frequency_hz must be a positive number", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	instance := s.monitor
	s.mu.Unlock()
	if instance == nil {
		http.Error(w, "no monitor is running", http.StatusNotFound)
		return
	}
	queued := instance.RequestListen(hz, r.FormValue("mode"))
	writeJSON(w, map[string]any{"tuned_hz": hz, "queued": queued})
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
	return s.ServeContext(context.Background(), addr)
}

// ServeContext serves until ctx is cancelled, then gracefully drains requests.
func (s *Server) ServeContext(ctx context.Context, addr string) error {
	if os.Getenv("SDRSTREAM_UI_PASSWORD") == "" && !s.NoAuth {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			host = addr
		}
		ip := net.ParseIP(host)
		loopback := (ip != nil && ip.IsLoopback()) || strings.EqualFold(host, "localhost")
		if !loopback {
			return fmt.Errorf("web: refusing to serve %q without SDRSTREAM_UI_PASSWORD: set a password or pass -no-auth to run an open UI", addr)
		}
		log.Printf("web: WARNING: no SDRSTREAM_UI_PASSWORD - serving an open UI on loopback only; set a password before exposing this beyond the local machine")
	}
	log.Printf("web UI on http://%s", addr)
	server := &http.Server{Addr: addr, Handler: s.Handler()}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.ListenAndServe() }()
	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
			return err
		}
		err := <-serveErr
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// sourcesResponse is the on-demand data-source health table.
type sourcesResponse struct {
	UpdatedAt time.Time        `json:"updated_at"`
	Running   bool             `json:"running"`
	Results   []sources.Result `json:"results"`
}

// handleSources serves the last health sweep. A sweep starts on the first
// request, or when ?refresh=1 arrives and the last sweep finished more than
// 30s ago - never on a timer, so the external services are only probed when
// someone actually asks.
func (s *Server) handleSources(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	stale := time.Since(s.sourcesAt) > 30*time.Second
	if !s.sourcesRunning && stale && (r.URL.Query().Get("refresh") == "1" || len(s.sourcesResults) == 0) {
		s.sourcesRunning = true
		go s.runSourcesSweep()
	}
	response := sourcesResponse{
		UpdatedAt: s.sourcesAt,
		Running:   s.sourcesRunning,
		Results:   append([]sources.Result(nil), s.sourcesResults...),
	}
	s.mu.Unlock()
	if response.Results == nil {
		response.Results = []sources.Result{}
	}
	writeJSON(w, response)
}

func (s *Server) runSourcesSweep() {
	s.mu.Lock()
	fn := s.healthAll
	s.mu.Unlock()
	if fn == nil {
		fn = sources.HealthAll
	}
	results := fn(context.Background(), sources.Options{Timeout: 10 * time.Second, Concurrency: 4})
	s.mu.Lock()
	s.sourcesResults = results
	s.sourcesAt = time.Now()
	s.sourcesRunning = false
	s.mu.Unlock()
}

// bandcondResponse serves the advisory band conditions; stale marks a cache
// served after a failed NOAA refresh, error carries the failure.
type bandcondResponse struct {
	bandcond.Conditions
	Stale bool   `json:"stale,omitempty"`
	Err   string `json:"error,omitempty"`
}

// handleBandConditions serves the cached NOAA-derived band conditions,
// refreshing at most every 30 minutes. NOAA failures never blank a good
// cache; they mark it stale with the error attached.
func (s *Server) handleBandConditions(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	stale := time.Since(s.condAt) > 30*time.Minute
	s.mu.Unlock()
	if stale {
		s.mu.Lock()
		fn := s.fetchConditions
		s.mu.Unlock()
		if fn == nil {
			fn = func(ctx context.Context) (bandcond.Conditions, error) {
				return bandcond.Fetch(ctx, "", 8*time.Second)
			}
		}
		conditions, err := fn(r.Context())
		s.mu.Lock()
		if err != nil {
			s.condErr = err.Error()
			// Count a failure against the refresh window too, so an
			// unreachable NOAA is retried later, not on every poll.
			s.condAt = time.Now()
		} else {
			s.cond = conditions
			s.condErr = ""
			s.condAt = time.Now()
		}
		s.mu.Unlock()
	}
	s.mu.Lock()
	response := bandcondResponse{Conditions: s.cond, Err: s.condErr}
	cachedAt := s.condAt
	s.mu.Unlock()
	response.UpdatedAt = cachedAt
	if response.Err != "" && len(response.Bands) > 0 {
		response.Stale = true
	}
	if len(response.Bands) == 0 {
		response.Bands = []bandcond.BandCondition{}
		response.Advisory = true
	}
	writeJSON(w, response)
}
