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
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/bandcond"
	"github.com/chaim12345/sdrstream/go/internal/catalogue"
	"github.com/chaim12345/sdrstream/go/internal/common"
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
	Satellite   string  `json:"satellite"`
	Receiver    string  `json:"receiver"`
	FrequencyHz float64 `json:"frequency_hz,omitempty"`
	Mode        string  `json:"mode,omitempty"`
	Decode      string  `json:"decode,omitempty"`
	Kind        string  `json:"kind,omitempty"`   // voice, data, cw, carrier, noise
	Target      string  `json:"target,omitempty"` // named service the clip was aimed at
	Verified    bool    `json:"verified"`         // live quality gate verdict (legacy summary flag)
	Gate        string  `json:"gate,omitempty"`   // gate reason when unverified
	// GateVerdict and Crosscheck keep the two proof facets separate:
	// the Go live quality gate's own verdict (feeds.GatePassed /
	// feeds.GateFailed, empty = not preserved) versus an independent
	// reference cross-check (feeds.Crosscheck*, empty = never performed).
	GateVerdict      string    `json:"gate_verdict,omitempty"`
	Crosscheck       string    `json:"crosscheck,omitempty"`
	CrosscheckRef    string    `json:"crosscheck_ref,omitempty"`
	CrosscheckDetail string    `json:"crosscheck_detail,omitempty"`
	Start            time.Time `json:"start"`
	Samples          int       `json:"samples"`
	Rate             int       `json:"rate"`
	Path             string    `json:"path"`
	URL              string    `json:"url,omitempty"`
}

// CaptureMetadata is the provenance written beside a capture. The scanner
// writes it for every archived clip, including unverified and non-decodable
// activity, so "record everything" still produces a searchable archive.
type CaptureMetadata struct {
	FrequencyHz float64
	Mode        string
	Receiver    string
	Detail      string
	Kind        string
	Target      string
	Gate        string
	// GateVerdict / Crosscheck / CrosscheckRef: the separate proof facets
	// described on Capture. Empty values keep the legacy sidecar format.
	GateVerdict      string
	Crosscheck       string
	CrosscheckRef    string
	CrosscheckDetail string
	Started          time.Time
	DurationSec      float64
	Verified         bool
}

// Status is the live snapshot the UI polls.
type Status struct {
	UpdatedAt  time.Time       `json:"updated_at"`
	Captures   []Capture       `json:"captures"`
	Passes     []PassInfo      `json:"passes"`
	Satellites []SatelliteInfo `json:"satellites"`
	Receivers  int             `json:"receivers"`
	// Network reports the startup connectivity self-check so an
	// unreachable network reads as failed, not as empty success.
	// Nil means the check has not completed yet.
	NetworkOK     *bool  `json:"network_ok,omitempty"`
	NetworkDetail string `json:"network_detail,omitempty"`
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
	// Status/Notes carry the catalogue's honesty about the bird (e.g.
	// decommissioned, TX unvalidated) so a pass is never presented as
	// readiness to receive.
	Status string `json:"status,omitempty"`
	Notes  string `json:"notes,omitempty"`
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
	// Notes carries the catalogue's source-grounded status detail (e.g.
	// NOAA OSPO decommission dates) - it must reach the UI, not die here.
	Notes string `json:"notes,omitempty"`
	// Event names the dated transmission event currently overriding the
	// catalogue downlink (e.g. an ARISS SSTV series on 70cm).
	Event           string `json:"event,omitempty"`
	EventDownlinkHz int64  `json:"event_downlink_hz,omitempty"`
	EventMode       string `json:"event_mode,omitempty"`
	EventEndUTC     string `json:"event_end_utc,omitempty"`
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

// obsCacheEntry is one cached SatNOGS observations lookup.
type obsCacheEntry struct {
	observations []satnogs.Observation
	fetchedAt    time.Time
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
	// Origin records where this row came from: "live" when a scanner
	// produced it in this process, "restored" when it was loaded from the
	// saved state file at startup. Restored rows are history, never
	// current-session activity.
	Origin string `json:"origin,omitempty"`
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
	satsched          interface{ StatusesJSON() interface{} }
	captures          []Capture
	passes            []PassInfo
	gallery           []gallery.Item
	liveGallery       []LiveGalleryItem
	html              string
	capturesDir       string
	galleryDir        string
	galleryAt         time.Time
	monitor           *monitor.Monitor
	monitorHooks      MonitorHooks
	networkOK         *bool
	networkDetail     string
	logRing           *LogRing
	feeds             *feeds.Manager
	scans             []ScanStatus
	scanIndex         map[string]int
	scanStatePath     string
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
	// capturesCache memoises the captures-directory listing: the UI polls
	// status.json constantly and wavInfo opens every file each time.
	capturesCache   []Capture
	capturesCacheAt time.Time
	// receiverLedgerSeen remembers which archived captures already have a
	// proof-ledger entry (keyed by archive filename); receiverLedgerLoaded
	// marks the one-time seed from the ledger file after a restart.
	receiverLedgerSeen   map[string]bool
	receiverLedgerLoaded bool
	// obsCache memoises SatNOGS observation lookups per NORAD id so the
	// map popups do not hit the network API (or disk cache) on every open.
	obsCache map[int]obsCacheEntry
	// lastScanPersist and scanStateTimer debounce scan-state writes: scan
	// updates arrive in bursts and each one used to rewrite the file.
	lastScanPersist time.Time
	scanStateTimer  *time.Timer
	// healthAll and fetchConditions are seams for the tests.
	healthAll       func(ctx context.Context, opts sources.Options) []sources.Result
	fetchConditions func(ctx context.Context) (bandcond.Conditions, error)
	// fetchStations is directory.FetchStations in production and a seam
	// for the tests, which cannot wait on the public directory.
	fetchStations func(context.Context) ([]directory.Station, error)
	// fetchObservations is satnogs.Observations in production and a seam
	// for the tests, which cannot wait on the network API.
	fetchObservations func(noradID int) ([]satnogs.Observation, error)
	NoAuth            bool
	auth              authState
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
	s := &Server{capturesDir: capturesDir, galleryDir: galleryDir, scanIndex: make(map[string]int), scanStatePath: scanStateFile()}
	if capturesDir != "" {
		go s.retentionLoop()
	}
	s.loadScanState()
	s.loadGallery()
	s.loadLiveGallery()
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

// passWindowHours is how far ahead the pass table looks.
const passWindowHours = 24

// passMinElevation is the default minimum peak elevation for the table, in
// degrees; /api/passes.json accepts a min_elevation override.
const passMinElevation = 10.0

// TLEAttribution credits the element-set source, per CelesTrak's terms
// (data free to use with attribution).
const TLEAttribution = "Orbit data: CelesTrak (celestrak.org), used with attribution"

// loadPasses predicts passes for the next 24 hours over good receivers and
// swaps the table atomically, so a refresh never shows a half-built list.
// Element sets load per CelesTrak group (amateur, weather), each through the
// disk cache, so one unreachable group only costs its own satellites.
func (s *Server) loadPasses() {
	texts := map[string]string{}
	for _, group := range []string{"amateur", "weather"} {
		text, err := tle.Load(group, 24, false, 60*time.Second)
		if err != nil {
			log.Printf("web: tle load %s: %v", group, err)
			continue
		}
		texts[group] = text
	}
	if len(texts) == 0 {
		return
	}
	var passes []PassInfo
	for _, sat := range satellites.All() {
		if sat.Status != "active" {
			continue
		}
		group := sat.Group
		if group == "" {
			group = "amateur"
		}
		text, ok := texts[group]
		if !ok {
			continue
		}
		receivers := catalogue.ForFrequency(float64(sat.DownlinkHz))
		if len(receivers) == 0 {
			continue
		}
		tracker, err := tracking.FromTLEText(text, sat.Name)
		if err != nil && sat.NoradID > 0 {
			// Decommissioned birds drop out of their CelesTrak group but
			// are still tracked by catalogue number.
			if single, catErr := tle.LoadCatnr(sat.NoradID, 24, false, 60*time.Second); catErr == nil && single != "" {
				tracker, err = tracking.FromTLEText(single, sat.Name)
			}
		}
		if err != nil {
			continue
		}
		for _, receiver := range receivers[:2] {
			passesForReceiver, err := tracker.FindPasses(receiver.Latitude, receiver.Longitude,
				time.Now(), passWindowHours, passMinElevation)
			if err != nil {
				continue
			}
			for _, pass := range passesForReceiver {
				passes = append(passes, PassInfo{
					Satellite: sat.Name, NoradID: sat.NoradID, Receiver: receiver.URL,
					Rise: pass.Rise, Peak: pass.Peak.Time, Set: pass.Set,
					MaxElevation: pass.MaxElevation(),
					Status:       sat.Status, Notes: sat.Notes,
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

// AddCaptureMetadata persists compatible, sanitized provenance atomically.
func (s *Server) AddCaptureMetadata(name, mode, receiver, detail string, frequencyHz float64, verified ...bool) {
	v := len(verified) > 0 && verified[0]
	_ = s.AddCaptureMetadataFull(name, CaptureMetadata{FrequencyHz: frequencyHz, Mode: mode, Receiver: receiver, Detail: detail, Verified: v})
}
func (s *Server) AddCaptureMetadataFull(name string, meta CaptureMetadata) error {
	return WriteCaptureMetadata(s.capturesDir, name, meta)
}
func WriteCaptureMetadata(dir, name string, meta CaptureMetadata) error {
	if dir == "" || filepath.Base(name) != name {
		return fmt.Errorf("web: capture name %q must be a bare filename", name)
	}
	if meta.Started.IsZero() {
		meta.Started = time.Now().UTC()
	}
	clean := func(v string) string {
		return strings.Map(func(r rune) rune {
			if r == '\r' || r == '\n' {
				return -1
			}
			return r
		}, v)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "frequency_hz=%.0f\nmode=%s\nreceiver=%s\ndecode=%s\nkind=%s\ntarget=%s\ngate=%s\nstarted=%s\nduration_sec=%.3f\nverified=%t\n", meta.FrequencyHz, clean(meta.Mode), clean(meta.Receiver), clean(meta.Detail), clean(meta.Kind), clean(meta.Target), clean(meta.Gate), meta.Started.UTC().Format(time.RFC3339), meta.DurationSec, meta.Verified)
	if meta.GateVerdict != "" {
		fmt.Fprintf(&b, "gate_verdict=%s\n", clean(meta.GateVerdict))
	}
	if meta.Crosscheck != "" {
		fmt.Fprintf(&b, "crosscheck=%s\n", clean(meta.Crosscheck))
	}
	if meta.CrosscheckRef != "" {
		fmt.Fprintf(&b, "crosscheck_ref=%s\n", clean(meta.CrosscheckRef))
	}
	if meta.CrosscheckDetail != "" {
		fmt.Fprintf(&b, "crosscheck_detail=%s\n", clean(meta.CrosscheckDetail))
	}
	return common.WriteFileAtomic(filepath.Join(dir, strings.TrimSuffix(name, ".wav")+".txt"), []byte(b.String()), 0o644)
}

// GateVerdictFor renders the Go live quality gate's boolean outcome as the
// explicit facet value written to new sidecars.
func GateVerdictFor(live bool) string {
	if live {
		return feeds.GatePassed
	}
	return feeds.GateFailed
}

// AddCapture records a completed capture for the UI.
func (s *Server) AddCapture(capture Capture) {
	s.mu.Lock()
	s.captures = append(s.captures, capture)
	s.mu.Unlock()
}

// capturesCacheTTL is how long a directory listing is reused. The UI polls
// every second; reading every WAV header on each poll kept a core busy.
const capturesCacheTTL = 2 * time.Second

// maxListedCaptures caps one directory listing.
const maxListedCaptures = 5000

// statusCaptureLimit bounds the capture table embedded in every status
// poll; the full list is available, paginated, from /api/captures.json.
const statusCaptureLimit = 200

// scanCaptures lists the WAV captures, memoised briefly: recordings appear
// at most capturesCacheTTL late, which a one-second UI poll never notices.
func (s *Server) scanCaptures() []Capture {
	if s.capturesDir == "" {
		return nil
	}
	s.mu.Lock()
	if s.capturesCache != nil && time.Since(s.capturesCacheAt) < capturesCacheTTL {
		out := append([]Capture(nil), s.capturesCache...)
		s.mu.Unlock()
		return out
	}
	s.mu.Unlock()
	out := s.listCaptures()
	s.syncReceiverLedger(out)
	s.mu.Lock()
	s.capturesCache = out
	s.capturesCacheAt = time.Now()
	s.mu.Unlock()
	return out
}

// listCaptures reads every WAV in the captures directory as a Capture.
// The satellite name is the leading part of the filename ("CAS-9_...wav"),
// the sample rate comes from the WAV header, and the size gives the sample
// count (16-bit mono). In-flight atomic writes ("<name>.wav.tmp") are
// skipped, and the listing is capped so an overflowing directory cannot
// turn one poll into unbounded work; retention (see retention.go) keeps
// real directories far below the cap.
func (s *Server) listCaptures() []Capture {
	entries, err := os.ReadDir(s.capturesDir)
	if err != nil {
		return nil
	}
	var out []Capture
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || strings.HasSuffix(name, ".tmp") || !strings.HasSuffix(strings.ToLower(name), ".wav") {
			continue
		}
		if len(out) >= maxListedCaptures {
			break
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
		mode, receiver, detail, kind, target, gate := "", "sdrstream", "", "", "", ""
		verified := false
		gateVerdict, crosscheck, crosscheckRef, crosscheckDetail := "", "", "", ""
		started := info.ModTime().UTC()
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
				switch {
				case strings.HasPrefix(line, "frequency_hz="):
					freq, _ = strconv.ParseFloat(strings.TrimPrefix(line, "frequency_hz="), 64)
				case strings.HasPrefix(line, "mode="):
					mode = strings.TrimPrefix(line, "mode=")
				case strings.HasPrefix(line, "receiver="):
					receiver = strings.TrimPrefix(line, "receiver=")
				case strings.HasPrefix(line, "decode="):
					detail = strings.TrimPrefix(line, "decode=")
				case strings.HasPrefix(line, "kind="):
					kind = strings.TrimPrefix(line, "kind=")
				case strings.HasPrefix(line, "target="):
					target = strings.TrimPrefix(line, "target=")
				case strings.HasPrefix(line, "gate="):
					gate = strings.TrimPrefix(line, "gate=")
				case strings.HasPrefix(line, "gate_verdict="):
					gateVerdict = strings.TrimPrefix(line, "gate_verdict=")
				case strings.HasPrefix(line, "crosscheck="):
					crosscheck = strings.TrimPrefix(line, "crosscheck=")
				case strings.HasPrefix(line, "crosscheck_ref="):
					crosscheckRef = strings.TrimPrefix(line, "crosscheck_ref=")
				case strings.HasPrefix(line, "crosscheck_detail="):
					crosscheckDetail = strings.TrimPrefix(line, "crosscheck_detail=")
				case line == "verified=true":
					verified = true
				case line == "verified=false":
					verified = false
				case strings.HasPrefix(line, "started="):
					if parsed, err := time.Parse(time.RFC3339, strings.TrimPrefix(line, "started=")); err == nil {
						started = parsed.UTC()
					}
				}
			}
		}
		out = append(out, Capture{
			Satellite:   satellite,
			Receiver:    receiver,
			FrequencyHz: freq, Mode: mode, Decode: detail,
			Kind: kind, Target: target, Verified: verified, Gate: gate,
			GateVerdict: gateVerdict, Crosscheck: crosscheck, CrosscheckRef: crosscheckRef, CrosscheckDetail: crosscheckDetail,
			Start:   started,
			Samples: int(dataBytes / 2),
			Rate:    rate,
			Path:    name,
			URL:     "/audio/" + url.PathEscape(name),
		})
	}
	// A sidecar whose WAV is gone is still proof: the decode text survives
	// even though the audio does not. List it with no URL and no samples so
	// the UI shows the decode with an "audio unavailable" label instead of
	// either hiding the proof or offering a player that cannot play.
	seen := map[string]bool{}
	for _, c := range out {
		seen[c.Path] = true
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".txt") {
			continue
		}
		base := strings.TrimSuffix(name, ".txt")
		if seen[base+".wav"] {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		var freq float64
		mode, receiver, detail, kind, target, gate := "", "sdrstream", "", "", "", ""
		verified := false
		gateVerdict, crosscheck, crosscheckRef, crosscheckDetail := "", "", "", ""
		started := info.ModTime().UTC()
		// A surviving sidecar is proof only if it is one of our metadata
		// files: hfschedule-style archives keep the raw decode text in
		// <key>.txt and the reference decode in <key>.ref.txt, neither of
		// which is a key=value sidecar. Require at least one known key so
		// raw text is never mis-listed as a capture proof.
		if sidecar, err := os.ReadFile(filepath.Join(s.capturesDir, name)); err == nil {
			known := false
			for _, line := range strings.Split(string(sidecar), "\n") {
				switch {
				case strings.HasPrefix(line, "frequency_hz="):
					known = true
				case strings.HasPrefix(line, "mode="):
					known = true
				case strings.HasPrefix(line, "receiver="):
					known = true
				case strings.HasPrefix(line, "decode="):
					known = true
				case strings.HasPrefix(line, "kind="):
					known = true
				case strings.HasPrefix(line, "target="):
					known = true
				case strings.HasPrefix(line, "gate="):
					known = true
				case strings.HasPrefix(line, "gate_verdict="):
					known = true
				case strings.HasPrefix(line, "crosscheck="):
					known = true
				case strings.HasPrefix(line, "verified="):
					known = true
				case strings.HasPrefix(line, "started="):
					known = true
				}
				switch {
				case strings.HasPrefix(line, "frequency_hz="):
					freq, _ = strconv.ParseFloat(strings.TrimPrefix(line, "frequency_hz="), 64)
				case strings.HasPrefix(line, "mode="):
					mode = strings.TrimPrefix(line, "mode=")
				case strings.HasPrefix(line, "receiver="):
					receiver = strings.TrimPrefix(line, "receiver=")
				case strings.HasPrefix(line, "decode="):
					detail = strings.TrimPrefix(line, "decode=")
				case strings.HasPrefix(line, "kind="):
					kind = strings.TrimPrefix(line, "kind=")
				case strings.HasPrefix(line, "target="):
					target = strings.TrimPrefix(line, "target=")
				case strings.HasPrefix(line, "gate="):
					gate = strings.TrimPrefix(line, "gate=")
				case strings.HasPrefix(line, "gate_verdict="):
					gateVerdict = strings.TrimPrefix(line, "gate_verdict=")
				case strings.HasPrefix(line, "crosscheck="):
					crosscheck = strings.TrimPrefix(line, "crosscheck=")
				case strings.HasPrefix(line, "crosscheck_ref="):
					crosscheckRef = strings.TrimPrefix(line, "crosscheck_ref=")
				case strings.HasPrefix(line, "crosscheck_detail="):
					crosscheckDetail = strings.TrimPrefix(line, "crosscheck_detail=")
				case line == "verified=true":
					verified = true
				case strings.HasPrefix(line, "started="):
					if parsed, err := time.Parse(time.RFC3339, strings.TrimPrefix(line, "started=")); err == nil {
						started = parsed.UTC()
					}
				}
			}
			if !known {
				continue
			}
		}
		satellite := base
		if i := strings.IndexByte(base, '_'); i > 0 {
			satellite = base[:i]
		}
		out = append(out, Capture{
			Satellite: satellite, Receiver: receiver,
			FrequencyHz: freq, Mode: mode, Decode: detail,
			Kind: kind, Target: target, Verified: verified, Gate: gate,
			GateVerdict: gateVerdict, Crosscheck: crosscheck, CrosscheckRef: crosscheckRef, CrosscheckDetail: crosscheckDetail,
			Start: started,
			Path:  name, // the surviving sidecar, as the ledger identity
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
		// script-src has no 'unsafe-inline': every script the page runs is
		// a bundled file under /static (pinned by TestCSPDropsUnsafeInlineScripts).
		// style-src keeps 'unsafe-inline' for the login page's inline style.
		header.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; media-src 'self'; connect-src 'self'; manifest-src 'self'; worker-src 'self'; base-uri 'none'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

// limitRequestBody caps POST/PUT forms. Every control the UI offers is a
// few hundred bytes; an unbounded body parser is a free memory-amplifier
// for anyone with a bored HTTP client.
func limitRequestBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil && (r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch) {
			r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		}
		next.ServeHTTP(w, r)
	})
}

//go:embed static
var staticFS embed.FS

// legalFS holds copies of the repository LICENSE, NOTICE and
// NOTICE-third-party.md (go:embed cannot reach outside this package).
// TestLegalFilesMatchRepoRoot keeps them identical to the root files.
//
//go:embed legal
var legalFS embed.FS

// handleLegal serves an embedded legal text as plain text.
func handleLegal(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data, err := legalFS.ReadFile("legal/" + name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write(data)
	}
}

// handlePWAFile serves one embedded static file at a root-level path with an
// explicit content type. The service worker is served no-cache so browsers
// pick up updates on the next load (its byte comparison drives updates).
func handlePWAFile(name, contentType string, noCache bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data, err := staticFS.ReadFile("static/" + name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", contentType)
		if noCache {
			w.Header().Set("Cache-Control", "no-cache")
		}
		_, _ = w.Write(data)
	}
}

// Handler returns the HTTP routes.
func (s *Server) Handler() http.Handler {
	s.auth.init()
	mux := http.NewServeMux()
	mux.HandleFunc("/login", s.handleLogin)
	mux.HandleFunc("/logout", s.handleLogout)
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/LICENSE", handleLegal("LICENSE"))
	mux.HandleFunc("/NOTICE", handleLegal("NOTICE"))
	mux.HandleFunc("/NOTICE-third-party.md", handleLegal("NOTICE-third-party.md"))
	mux.HandleFunc("/api/status.json", s.handleStatus)
	mux.HandleFunc("/api/logs/tail", s.handleLogTail)
	mux.HandleFunc("/api/captures.json", s.handleCaptures)
	mux.HandleFunc("/api/satellites.json", s.handleSatellites)
	mux.HandleFunc("/api/passes.json", s.handlePasses)
	mux.HandleFunc("/api/satfollow.json", s.handleSatfollow)
	mux.HandleFunc("/api/observations.json", s.handleObservations)
	mux.HandleFunc("/api/gallery.json", s.handleGallery)
	mux.HandleFunc("/api/test-fixtures.json", s.handleTestFixtures)
	mux.HandleFunc("/api/scans.json", s.handleScans)
	mux.HandleFunc("/api/feeds.json", s.handleFeeds)
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/api/ledger.json", s.handleLedger)
	mux.HandleFunc("/api/ledger.csv", s.handleLedgerCSV)
	mux.HandleFunc("/api/metrics.json", s.handleMetrics)
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
	mux.HandleFunc("/manifest.webmanifest", handlePWAFile("manifest.webmanifest", "application/manifest+json", false))
	mux.HandleFunc("/sw.js", handlePWAFile("sw.js", "text/javascript; charset=utf-8", true))
	mux.Handle("/static/", http.FileServerFS(staticFS))
	mux.HandleFunc("/audio/", s.handleAudio)
	mux.HandleFunc("/audio/live/", s.handleLiveAudio)
	mux.HandleFunc("/gallery/", s.handleGalleryFile)
	return limitRequestBody(securityHeaders(s.wrapAuth(mux)))
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
	if len(captures) > statusCaptureLimit {
		captures = captures[len(captures)-statusCaptureLimit:]
	}
	s.mu.Lock()
	networkOK, networkDetail := s.networkOK, s.networkDetail
	s.mu.Unlock()
	writeJSON(w, Status{
		UpdatedAt: time.Now(), Captures: captures,
		Passes: passes, Receivers: len(catalogue.Table),
		NetworkOK: networkOK, NetworkDetail: networkDetail,
	})
}

// handleCaptures serves the capture table one page at a time: ?limit=
// (default 200, at most 1000) and ?offset= select the window and the
// X-Total-Captures header reports the full count, so a client can page
// without the server ever serialising the whole history at once.
func (s *Server) handleCaptures(w http.ResponseWriter, r *http.Request) {
	captures := s.scanCaptures()
	s.mu.Lock()
	captures = append(captures, s.captures...)
	s.mu.Unlock()
	sort.Slice(captures, func(i, j int) bool { return captures[i].Start.Before(captures[j].Start) })
	total := len(captures)
	limit := 200
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 1000 {
		limit = 1000
	}
	offset := 0
	if raw := r.URL.Query().Get("offset"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			offset = n
		}
	}
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	page := captures[offset:end]
	if page == nil {
		page = []Capture{}
	}
	w.Header().Set("X-Total-Captures", strconv.Itoa(total))
	writeJSON(w, page)
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
	// Spectrogram evidence is generated offline from the saved WAV and
	// cached beside it; the audio itself never leaves this directory.
	if strings.HasSuffix(name, ".wav.spec.png") {
		pngBytes, err := s.spectrogramFor(strings.TrimSuffix(name, ".spec.png"))
		if err != nil {
			http.Error(w, "spectrogram unavailable: "+err.Error(), http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = w.Write(pngBytes)
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
	if err := common.WriteFileAtomic(filepath.Join(s.galleryDir, name), data, 0o644); err != nil {
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
			if err := common.WriteFileAtomic(filepath.Join(s.galleryDir, audioName), src, 0o644); err == nil {
				item.AudioURL = "/audio/live/" + url.PathEscape(audioName)
			}
		}
	}
	s.mu.Lock()
	s.liveGallery = append([]LiveGalleryItem{item}, s.liveGallery...)
	var pruned []LiveGalleryItem
	if len(s.liveGallery) > liveGalleryCap {
		pruned = append([]LiveGalleryItem(nil), s.liveGallery[liveGalleryCap:]...)
		s.liveGallery = s.liveGallery[:liveGalleryCap]
	}
	if err := s.persistLiveGalleryLocked(); err != nil {
		log.Printf("gallery: persist live index: %v", err)
	}
	hook := s.rdioHook
	s.mu.Unlock()
	for _, old := range pruned {
		if filepath.Base(old.Name) == old.Name {
			_ = os.Remove(filepath.Join(s.galleryDir, old.Name))
			if old.AudioURL != "" {
				_ = os.Remove(filepath.Join(s.galleryDir, old.Name+".wav"))
			}
		}
	}
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

// liveGalleryCap bounds how many verified live artefacts the gallery keeps.
const liveGalleryCap = 200

// liveGalleryIndexFile is the on-disk manifest that lets live artifacts
// survive an engine restart (files alone were unreachable: the serving
// routes gate on the in-memory list).
const liveGalleryIndexFile = "live-index.json"

// persistLiveGalleryLocked writes the live gallery manifest atomically.
// Callers must hold s.mu.
func (s *Server) persistLiveGalleryLocked() error {
	if s.galleryDir == "" {
		return nil
	}
	data, err := json.Marshal(s.liveGallery)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.galleryDir, 0o755); err != nil {
		return err
	}
	return common.WriteFileAtomic(filepath.Join(s.galleryDir, liveGalleryIndexFile), data, 0o644)
}

// loadLiveGallery restores the live gallery manifest written by previous
// runs. Items whose artifact file vanished are dropped; items whose audio
// file vanished keep their text/image but lose audio_url so the UI shows
// "audio unavailable" instead of a player that 404s.
func (s *Server) loadLiveGallery() {
	if s.galleryDir == "" {
		return
	}
	data, err := os.ReadFile(filepath.Join(s.galleryDir, liveGalleryIndexFile))
	if err != nil {
		return
	}
	var items []LiveGalleryItem
	if err := json.Unmarshal(data, &items); err != nil {
		log.Printf("gallery: read live index: %v", err)
		return
	}
	kept := make([]LiveGalleryItem, 0, len(items))
	for _, item := range items {
		if item.Name == "" || filepath.Base(item.Name) != item.Name {
			continue
		}
		if _, err := os.Stat(filepath.Join(s.galleryDir, item.Name)); err != nil {
			continue
		}
		if item.AudioURL != "" {
			audioName := item.Name + ".wav"
			if _, err := os.Stat(filepath.Join(s.galleryDir, audioName)); err != nil {
				item.AudioURL = ""
			}
		}
		kept = append(kept, item)
		if len(kept) >= liveGalleryCap {
			break
		}
	}
	s.mu.Lock()
	s.liveGallery = kept
	s.mu.Unlock()
}

// ServeExtra serves the same authenticated handler on a second address,
// e.g. a tailnet-only IP for remote access without exposing the UI on
// every interface. It refuses empty/loopback-duplicate addresses; the
// caller is responsible for requiring a password before binding a
// non-loopback address.
func (s *Server) ServeExtra(ctx context.Context, addr string) error {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	server := s.newHTTPServer(addr)
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	go func() { _ = server.Serve(listener) }()
	log.Printf("web UI also on http://%s (password required)", addr)
	return nil
}

// LogRing is a capped ring of recent log lines, teed from the engine's
// log output so /api/logs/tail can serve diagnostics over the same
// authenticated UI - on a phone there is no other way to read them.
type LogRing struct {
	mu    sync.Mutex
	lines []string
	max   int
}

// NewLogRing returns a ring holding the last max lines.
func NewLogRing(max int) *LogRing {
	if max <= 0 {
		max = 500
	}
	return &LogRing{max: max}
}

var (
	// logURLRe matches absolute http(s) URLs so their userinfo and query
	// string (which is where private feed URLs carry approximate receiver
	// coordinates and access keys) can be stripped from the ring.
	logURLRe = regexp.MustCompile(`https?://[^\s"']+`)
	// logCredRe redacts key=value / key: value credentials, including
	// Cookie headers, wherever they appear in a log line.
	logCredRe = regexp.MustCompile(`(?i)\b(password|passwd|token|secret|api[-_]?key|session[-_]?id|cookie|authorization)(\s*[:=]\s*)("?)[^\s,"']*("?)`)
	// logCoordRe redacts keyed coordinates (lat=..., lon: ...) even
	// outside URLs. Bare decimal numbers stay: frequencies and levels are
	// legitimate diagnostics.
	logCoordRe = regexp.MustCompile(`(?i)\b(lat|lon|latitude|longitude)(\s*[:=]\s*)-?\d+(\.\d+)?`)
)

// redactLogLine strips credentials, cookies, URL queries/userinfo and
// keyed coordinates before a line enters the ring, so the tail endpoint
// can never serve them. The redaction is lossy by design: diagnostics
// need hosts and paths, never secrets or positions.
func redactLogLine(line string) string {
	line = logURLRe.ReplaceAllStringFunc(line, func(u string) string {
		if i := strings.Index(u, "://"); i >= 0 {
			rest := u[i+3:]
			if at := strings.Index(rest, "@"); at >= 0 && !strings.Contains(rest[:at], "/") {
				u = u[:i+3] + rest[at+1:]
			}
		}
		if q := strings.Index(u, "?"); q >= 0 {
			u = u[:q] + "?[redacted]"
		}
		return u
	})
	line = logCredRe.ReplaceAllString(line, "$1$2[redacted]")
	line = logCoordRe.ReplaceAllString(line, "$1$2[redacted]")
	return line
}

// Write implements io.Writer; each write appends whole redacted lines,
// truncated to 500 chars so one noisy line cannot dominate the tail.
func (r *LogRing) Write(p []byte) (int, error) {
	r.mu.Lock()
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		line = redactLogLine(line)
		if len(line) > 500 {
			line = line[:500]
		}
		r.lines = append(r.lines, line)
	}
	if len(r.lines) > r.max {
		r.lines = r.lines[len(r.lines)-r.max:]
	}
	r.mu.Unlock()
	return len(p), nil
}

// Tail returns up to n most recent lines, oldest first.
func (r *LogRing) Tail(n int) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n <= 0 || n > len(r.lines) {
		n = len(r.lines)
	}
	out := make([]string, n)
	copy(out, r.lines[len(r.lines)-n:])
	return out
}

// SetLogRing wires the engine log ring into the server.
func (s *Server) SetLogRing(ring *LogRing) {
	s.mu.Lock()
	s.logRing = ring
	s.mu.Unlock()
}

// handleLogTail serves recent engine log lines. Like every route but
// /login it sits behind the session gate, and every line passed through
// redactLogLine before entering the ring: no credentials, cookies, URL
// queries/userinfo (feed coordinates, keys) or keyed positions.
func (s *Server) handleLogTail(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	ring := s.logRing
	s.mu.Unlock()
	if ring == nil {
		http.Error(w, "log tail not configured", http.StatusNotFound)
		return
	}
	n := 100
	if raw := r.URL.Query().Get("lines"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil {
			n = v
		}
	}
	if n < 1 || n > 500 {
		http.Error(w, "lines must be 1..500", http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]interface{}{"lines": ring.Tail(n)})
}

func (s *Server) handleLiveAudio(w http.ResponseWriter, r *http.Request) {
	name, err := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/audio/live/"))
	if err != nil || name == "" || strings.ContainsAny(name, "/\\") {
		http.NotFound(w, r)
		return
	}
	// Exact-name match: a suffix match would let ".../clip.wav" serve the
	// audio of a different item whose name merely ends in "clip.wav".
	want := "/audio/live/" + url.PathEscape(name)
	s.mu.Lock()
	found := false
	for _, item := range s.liveGallery {
		if item.AudioURL == want {
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

// SetSatsched registers the pass scheduler's status provider. The provider
// interface is structural so web does not import the scheduler (which
// imports web).
func (s *Server) SetSatsched(provider interface{ StatusesJSON() interface{} }) {
	s.mu.Lock()
	s.satsched = provider
	s.mu.Unlock()
}

// handleSatfollow reports the automatic pass scheduler's decisions.
func (s *Server) handleSatfollow(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	provider := s.satsched
	s.mu.Unlock()
	if provider == nil {
		writeJSON(w, map[string]interface{}{"enabled": false})
		return
	}
	writeJSON(w, map[string]interface{}{"enabled": true, "statuses": provider.StatusesJSON()})
}

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

// scanStateFile resolves the scan-state path. Each Server pins it once at
// construction (New), so a debounced persist firing after a test's
// SDRSTREAM_STATE_DIR cleanup cannot leak rows into another user's state.
func scanStateFile() string { return stateFile("sdrstream-scan-state.json") }

// scanStatePathFor returns the Server's pinned scan-state path, resolving
// it on demand for Servers built directly as &Server{} in tests.
func (s *Server) scanStatePathFor() string {
	if s.scanStatePath != "" {
		return s.scanStatePath
	}
	return scanStateFile()
}

func (s *Server) loadScanState() {
	b, err := os.ReadFile(s.scanStatePathFor())
	if err != nil {
		return
	}
	var items []ScanStatus
	if json.Unmarshal(b, &items) != nil {
		return
	}
	for _, item := range items {
		item.Origin = "restored"
		s.UpsertScanStatus(item)
	}
}

// scanStatePersistInterval debounces scan-state writes: scan updates arrive
// in bursts of dozens per second, and each one used to rewrite the file.
const scanStatePersistInterval = 2 * time.Second

// persistScanStateLocked schedules the scan state to be written. The first
// change after a quiet interval writes immediately; further changes within
// the interval collapse into one trailing write. s.mu must be held.
func (s *Server) persistScanStateLocked() {
	if s.scanStateTimer != nil {
		// A trailing write is already scheduled and will see this change.
		return
	}
	since := time.Since(s.lastScanPersist)
	if since >= scanStatePersistInterval {
		s.writeScanStateLocked()
		return
	}
	s.scanStateTimer = time.AfterFunc(scanStatePersistInterval-since, s.flushScanState)
}

// writeScanStateLocked serialises and writes the scan state now. s.mu must
// be held.
func (s *Server) writeScanStateLocked() {
	b, err := json.Marshal(s.scans)
	if err != nil {
		return
	}
	s.lastScanPersist = time.Now()
	writeStateFile(s.scanStatePathFor(), b)
}

// ScanStatuses returns a copy of the current scan board rows, including the
// ones loaded from disk at startup.
func (s *Server) ScanStatuses() []ScanStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ScanStatus(nil), s.scans...)
}

// flushScanState writes any pending scan state immediately: on shutdown, so
// the debounced tail is not lost, and from tests.
func (s *Server) flushScanState() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.scanStateTimer != nil {
		s.scanStateTimer.Stop()
		s.scanStateTimer = nil
	}
	s.writeScanStateLocked()
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
	if item.Origin == "" {
		item.Origin = "live"
	}
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
	// Copy under the lock, build the payload after releasing it: sorting
	// and marshalling hundreds of rows blocked every scan update.
	s.mu.Lock()
	logCopy := append([]ScanStatus(nil), s.scanLog...)
	logSeq := s.scanLogSeq
	s.mu.Unlock()
	start := uint64(0)
	if uint64(len(logCopy)) <= logSeq {
		start = logSeq - uint64(len(logCopy))
	}
	items := make([]map[string]any, 0)
	for i, item := range logCopy {
		seq := start + uint64(i) + 1
		if seq > since {
			items = append(items, map[string]any{"id": seq, "station": item.Station, "station_type": item.StationType, "frequency_hz": item.FrequencyHz, "mode": item.Mode, "state": item.State, "reason": item.Result, "updated_at": item.UpdatedAt})
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return fmt.Sprint(items[i]["updated_at"]) < fmt.Sprint(items[j]["updated_at"]) })
	writeJSON(w, map[string]any{"items": items, "next_since": logSeq})
}

func (s *Server) handleScans(w http.ResponseWriter, r *http.Request) {
	// Copy under the lock, build the payload after releasing it: the
	// per-station band-condition pass blocked every scan update.
	s.mu.Lock()
	metrics := s.scanMetrics
	scans := append([]ScanStatus(nil), s.scans...)
	cond := s.cond
	condAt := s.condAt
	s.mu.Unlock()
	if metrics.TargetConcurrency == 0 {
		metrics.TargetConcurrency = metrics.RampStep
	}
	metrics.Stations = make([]ScanStatus, 0, len(scans))
	metrics.Dropped = make([]DroppedStation, 0)
	for _, item := range scans {
		if item.State == "blocked" || strings.Contains(strings.ToLower(item.State), "error") || strings.Contains(strings.ToLower(item.State), "timeout") || strings.Contains(strings.ToLower(item.State), "refused") || strings.Contains(strings.ToLower(item.State), "failed") {
			metrics.Dropped = append(metrics.Dropped, DroppedStation{Station: item.Station, Reason: item.Result, Until: item.BlockedUntil})
			continue
		}
		metrics.Stations = append(metrics.Stations, item)
	}
	if len(cond.Bands) > 0 && time.Since(condAt) < 3*time.Hour {
		for i := range metrics.Stations {
			if label, ok := cond.LabelForHz(metrics.Stations[i].FrequencyHz); ok {
				band, _ := bandcond.BandForHz(metrics.Stations[i].FrequencyHz)
				metrics.Stations[i].BandCondition = band + " " + label
			}
		}
	}
	// Deep-copied and annotated; the write happens without the lock so a
	// slow reader cannot stall the scanner's status updates.
	writeJSON(w, metrics)
}

// SetScanMetrics publishes live ramp and resource counters for the scanner.
func (s *Server) handleScanControl(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	if !sameOriginPOST(r) {
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
	if !sameOriginPOST(r) {
		http.Error(w, "same-origin required", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	switch r.FormValue("action") {
	case "start":
		s.handleMonitorStart(w, r)
		return
	case "stop":
		s.handleMonitorStop(w)
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

// monitorKinds are the receiver types the UI can start.
var monitorKinds = map[string]bool{"openwebrx": true, "websdr": true, "kiwi": true, "kiwisdr": true}

// handleMonitorStart starts a live monitor from UI-provided receiver
// details. Construction failures (bad kind, unreachable receiver,
// refused connection) come back as errors so the UI can show an honest
// "failed" state instead of pretending an empty panel is success.
func (s *Server) handleMonitorStart(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	hooks := s.monitorHooks
	s.mu.Unlock()
	if hooks.Start == nil {
		http.Error(w, "starting a monitor from the UI is not supported by this build", http.StatusNotImplemented)
		return
	}
	spec, err := parseMonitorStartSpec(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := hooks.Start(spec); err != nil {
		http.Error(w, "monitor failed to start: "+err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]interface{}{"running": true, "server": spec.Server, "kind": spec.Kind})
}

// handleMonitorStop detaches the running monitor. It is idempotent so a
// double tap never reports an error.
func (s *Server) handleMonitorStop(w http.ResponseWriter) {
	s.mu.Lock()
	hooks := s.monitorHooks
	s.mu.Unlock()
	if hooks.Stop == nil {
		http.Error(w, "stopping a monitor from the UI is not supported by this build", http.StatusNotImplemented)
		return
	}
	if err := hooks.Stop(); err != nil {
		http.Error(w, "monitor failed to stop: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]interface{}{"running": false})
}

var monitorServerPattern = regexp.MustCompile(`^[A-Za-z0-9._\-:\[\]]{3,200}$`)

// parseMonitorStartSpec validates the user-supplied receiver. The server
// field is a host[:port] (an http(s):// scheme the user pasted is
// stripped); anything else is rejected before it reaches a dialer.
func parseMonitorStartSpec(r *http.Request) (MonitorStartSpec, error) {
	spec := MonitorStartSpec{
		Server:  strings.TrimSpace(r.FormValue("server")),
		Kind:    strings.ToLower(strings.TrimSpace(r.FormValue("kind"))),
		Profile: strings.TrimSpace(r.FormValue("profile")),
		Label:   strings.TrimSpace(r.FormValue("label")),
	}
	if spec.Kind == "" {
		spec.Kind = "openwebrx"
	}
	if !monitorKinds[spec.Kind] {
		return spec, fmt.Errorf("kind must be openwebrx, websdr or kiwi")
	}
	if strings.HasPrefix(spec.Server, "http://") {
		spec.Server = strings.TrimPrefix(spec.Server, "http://")
	} else if strings.HasPrefix(spec.Server, "https://") {
		spec.Server = strings.TrimPrefix(spec.Server, "https://")
	}
	spec.Server = strings.TrimSuffix(spec.Server, "/")
	if !monitorServerPattern.MatchString(spec.Server) {
		return spec, fmt.Errorf("server must be a receiver host[:port], e.g. jukebox.spdns.eu:8073")
	}
	if len(spec.Profile) > 64 || strings.ContainsAny(spec.Profile, " \t\r\n\"'\\") {
		return spec, fmt.Errorf("invalid profile name")
	}
	if len(spec.Label) > 80 {
		spec.Label = spec.Label[:80]
	}
	if raw := strings.TrimSpace(r.FormValue("start_hz")); raw != "" {
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil || v <= 0 || v > 2e9 {
			return spec, fmt.Errorf("start_hz must be a frequency in Hz")
		}
		spec.StartHz = v
	}
	if raw := strings.TrimSpace(r.FormValue("end_hz")); raw != "" {
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil || v <= 0 || v > 2e9 {
			return spec, fmt.Errorf("end_hz must be a frequency in Hz")
		}
		spec.EndHz = v
	}
	if spec.StartHz > 0 && spec.EndHz > 0 && spec.EndHz <= spec.StartHz {
		return spec, fmt.Errorf("end_hz must be greater than start_hz")
	}
	return spec, nil
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

// MonitorStartSpec describes a live monitor the UI asks the server to
// start. Server is the receiver host[:port]; Kind is openwebrx, websdr
// or kiwi; Profile selects an OpenWebRX profile when the receiver has
// more than one.
type MonitorStartSpec struct {
	Server  string
	Kind    string
	Profile string
	Label   string
	// StartHz and EndHz optionally override the sweep range; they must
	// sit inside the receiver's coverage or tuning fails honestly.
	// Zero keeps the command's configured defaults.
	StartHz float64
	EndHz   float64
}

// MonitorHooks are registered by the embedding command, which owns
// monitor construction and teardown. Start replaces any monitor already
// running; Stop is idempotent. A nil function means the command does
// not support that runtime control.
type MonitorHooks struct {
	Start func(MonitorStartSpec) error
	Stop  func() error
}

// SetNetworkCheck records the result of the startup connectivity
// self-check; handleStatus reports it.
func (s *Server) SetNetworkCheck(ok bool, detail string) {
	s.mu.Lock()
	s.networkOK = &ok
	s.networkDetail = detail
	s.mu.Unlock()
}

// SetMonitorHooks installs runtime monitor control for /api/monitor/control.
func (s *Server) SetMonitorHooks(hooks MonitorHooks) {
	s.mu.Lock()
	s.monitorHooks = hooks
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
	live := append([]LiveGalleryItem(nil), s.liveGallery...)
	s.mu.Unlock()

	var items []monitor.Activity
	if instance != nil {
		items = instance.Activities()
	}
	// The activity pill is the UI's "verified traffic" total. Scanner and
	// feed decodes only live in the gallery, so merge them in (skipping
	// anything the monitor already reported; monitor decodes are also saved
	// to the gallery and would otherwise be counted twice).
	seen := make(map[string]bool, len(items))
	for _, a := range items {
		seen[activityDedupeKey(a)] = true
	}
	for i, it := range live {
		kind := "data"
		switch {
		case it.Kind == "image":
			kind = "image"
		case it.Mode == "speech" || it.Kind == "voice":
			kind = "voice"
		}
		a := monitor.Activity{
			ID:          i + 1,
			Started:     it.Start,
			FrequencyHz: float64(it.FrequencyHz),
			Kind:        kind,
			Mode:        it.Mode,
			Detail:      it.Text,
			Source:      it.Station,
		}
		if seen[activityDedupeKey(a)] {
			continue
		}
		items = append(items, a)
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Started.After(items[j].Started) })
	writeJSON(w, items)
}

func activityDedupeKey(a monitor.Activity) string {
	return fmt.Sprintf("%d|%.0f|%s|%s", a.Started.UnixNano(), a.FrequencyHz, a.Kind, a.Detail)
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
	if !sameOriginPOST(r) {
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
	// The mode goes straight to the receiver, so only the known listening
	// modes are accepted; empty keeps the monitor's default.
	mode := r.FormValue("mode")
	if mode != "" && !monitorModes[mode] {
		http.Error(w, "unknown mode", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	instance := s.monitor
	s.mu.Unlock()
	if instance == nil {
		http.Error(w, "no monitor is running", http.StatusNotFound)
		return
	}
	queued := instance.RequestListen(hz, mode)
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

// handlePasses serves the predicted-pass table with an optional
// min_elevation filter (degrees) and the CelesTrak attribution.
func (s *Server) handlePasses(w http.ResponseWriter, r *http.Request) {
	minElevation := 0.0
	if raw := r.URL.Query().Get("min_elevation"); raw != "" {
		if value, err := strconv.ParseFloat(raw, 64); err == nil {
			minElevation = value
		}
	}
	s.mu.Lock()
	passes := append([]PassInfo(nil), s.passes...)
	s.mu.Unlock()
	out := passes[:0]
	for _, pass := range passes {
		if pass.MaxElevation >= minElevation {
			out = append(out, pass)
		}
	}
	writeJSON(w, map[string]interface{}{
		"window_hours": passWindowHours, "attribution": TLEAttribution,
		"passes": out,
	})
}

func (s *Server) handleSatellites(w http.ResponseWriter, r *http.Request) {
	var out []SatelliteInfo
	for _, sat := range satellites.All() {
		relay := ""
		if relays.IsRelay(sat) && sat.Status == "active" {
			relay = string(relays.KindOf(sat))
		}
		band := "2m"
		switch {
		case sat.Group == "weather":
			band = "137mhz"
		case sat.DownlinkHz >= 420_000_000 && sat.DownlinkHz < 450_000_000:
			band = "70cm"
		case sat.DownlinkHz >= 1240_000_000:
			band = "23cm"
		}
		info := SatelliteInfo{
			Name: sat.Name, NoradID: sat.NoradID, DownlinkHz: sat.DownlinkHz,
			Mode: sat.Mode, Relay: relay, Status: sat.Status, Band: band,
			Receivers: len(catalogue.ForFrequency(float64(sat.DownlinkHz))),
			Notes:     sat.Notes,
		}
		if ev := satellites.ActiveEvent(sat.Name, time.Now()); ev != nil {
			info.Event = ev.Name
			info.EventDownlinkHz = ev.DownlinkHz
			info.EventMode = ev.ImageMode
			info.EventEndUTC = ev.EndUTC
		}
		out = append(out, info)
	}
	writeJSON(w, out)
}

// observationsCacheTTL is how long one satellite's observation list is
// reused; new SatNOGS observations land on timescales of minutes.
const observationsCacheTTL = 5 * time.Minute

// maxCachedObservationSets bounds the per-satellite observation cache.
const maxCachedObservationSets = 64

func (s *Server) handleObservations(w http.ResponseWriter, r *http.Request) {
	norad := r.URL.Query().Get("norad")
	if norad == "" {
		http.Error(w, "norad query parameter required", http.StatusBadRequest)
		return
	}
	var id int
	fmt.Sscanf(norad, "%d", &id)
	s.mu.Lock()
	if s.obsCache == nil {
		s.obsCache = make(map[int]obsCacheEntry)
	}
	entry, cached := s.obsCache[id]
	fetch := s.fetchObservations
	s.mu.Unlock()
	if cached && time.Since(entry.fetchedAt) < observationsCacheTTL {
		writeJSON(w, entry.observations)
		return
	}
	if fetch == nil {
		fetch = func(noradID int) ([]satnogs.Observation, error) {
			return satnogs.Observations(noradID, 15, 30*time.Second)
		}
	}
	observations, err := fetch(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	s.mu.Lock()
	// Evict the stalest entry when full so the map cannot grow per NORAD id
	// the UI ever asks about.
	if len(s.obsCache) >= maxCachedObservationSets {
		oldestID, oldestAt := 0, time.Now()
		for cacheID, cacheEntry := range s.obsCache {
			if cacheEntry.fetchedAt.Before(oldestAt) {
				oldestID, oldestAt = cacheID, cacheEntry.fetchedAt
			}
		}
		delete(s.obsCache, oldestID)
	}
	s.obsCache[id] = obsCacheEntry{observations: observations, fetchedAt: time.Now()}
	s.mu.Unlock()
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

// newHTTPServer builds the HTTP server with explicit timeouts. Without
// them a slow client can pin a connection (and its goroutine) forever:
// ReadHeaderTimeout bounds the header wait, ReadTimeout the whole request
// (generous enough for a login over a slow link), WriteTimeout must cover
// streaming a large WAV over a thin receiver link, and IdleTimeout reaps
// keep-alive connections gone quiet.
func (s *Server) newHTTPServer(addr string) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      10 * time.Minute,
		IdleTimeout:       120 * time.Second,
	}
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
	// Brokered defaults: slowloris-proof header read, bounded idle connections.
	// No global ReadTimeout beyond the header stage is set here; the JSON
	// and file handlers are all quick, but a hung scanner-writer connection
	// would otherwise be killed mid-response.

	defer s.flushScanState()
	server := s.newHTTPServer(addr)
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
