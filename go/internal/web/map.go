package web

// Map and tile endpoints for the UI overhaul. Both are read-only:
// /api/map.json aggregates receiver coordinates, live satellite positions
// and pass windows for the map view; /api/tiles/ proxies map tiles through
// the same origin so the strict Content-Security-Policy (img-src 'self')
// keeps working. The UI falls back to a drawn graticule when tiles fail.

import (
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/catalogue"
	"github.com/chaim12345/sdrstream/go/internal/satellites"
	"github.com/chaim12345/sdrstream/go/internal/tle"
	"github.com/chaim12345/sdrstream/go/internal/tracking"
)

// MapReceiver is one catalogue receiver plotted on the map.
type MapReceiver struct {
	URL            string   `json:"url"`
	Kind           string   `json:"kind"`
	Latitude       float64  `json:"latitude"`
	Longitude      float64  `json:"longitude"`
	SatelliteBands []string `json:"satellite_bands,omitempty"`
}

// MapSatellite is an active satellite with its current sub-point and a
// sampled ground track for roughly one orbit.
type MapSatellite struct {
	Name       string       `json:"name"`
	NoradID    int          `json:"norad_id"`
	DownlinkHz int64        `json:"downlink_hz"`
	Mode       string       `json:"mode"`
	Latitude   float64      `json:"latitude"`
	Longitude  float64      `json:"longitude"`
	AltitudeKm float64      `json:"altitude_km"`
	Track      [][2]float64 `json:"track"`
}

// MapPass is a predicted pass with the receiver's coordinates resolved, so
// the UI can draw the satellite-to-receiver path while it is in progress.
type MapPass struct {
	Satellite         string    `json:"satellite"`
	Receiver          string    `json:"receiver"`
	ReceiverLatitude  float64   `json:"receiver_latitude"`
	ReceiverLongitude float64   `json:"receiver_longitude"`
	Rise              time.Time `json:"rise"`
	Set               time.Time `json:"set"`
	MaxElevation      float64   `json:"max_elevation"`
	Active            bool      `json:"active"`
}

// MapData is the payload of /api/map.json.
type MapData struct {
	GeneratedAt         time.Time      `json:"generated_at"`
	SatellitesAvailable bool           `json:"satellites_available"`
	Receivers           []MapReceiver  `json:"receivers"`
	Satellites          []MapSatellite `json:"satellites"`
	Passes              []MapPass      `json:"passes"`
}

// Map cache lives at package level (like the session store) so the Server
// struct the integrator owns stays untouched.
var (
	mapCacheMu sync.Mutex
	mapCache   *MapData
	mapCacheAt time.Time
)

// handleMap serves the aggregated map payload, cached for a minute: the
// SGP4 propagation for every active satellite is fast but pointless to redo
// per browser poll.
func (s *Server) handleMap(w http.ResponseWriter, r *http.Request) {
	mapCacheMu.Lock()
	cached := mapCache
	fresh := time.Since(mapCacheAt) < time.Minute
	mapCacheMu.Unlock()
	if cached != nil && fresh {
		writeJSON(w, cached)
		return
	}
	data := s.buildMapData()
	mapCacheMu.Lock()
	mapCache = data
	mapCacheAt = time.Now()
	mapCacheMu.Unlock()
	writeJSON(w, data)
}

func (s *Server) buildMapData() *MapData {
	data := &MapData{
		GeneratedAt: time.Now(),
		Receivers:   []MapReceiver{},
		Satellites:  []MapSatellite{},
		Passes:      []MapPass{},
	}
	coords := map[string][2]float64{}
	for _, receiver := range catalogue.Table {
		if receiver.Latitude == 0 && receiver.Longitude == 0 {
			continue
		}
		data.Receivers = append(data.Receivers, MapReceiver{
			URL: receiver.URL, Kind: receiver.Kind,
			Latitude: receiver.Latitude, Longitude: receiver.Longitude,
			SatelliteBands: receiver.SatelliteBands,
		})
		coords[receiver.URL] = [2]float64{receiver.Latitude, receiver.Longitude}
	}

	s.mu.Lock()
	passes := append([]PassInfo(nil), s.passes...)
	s.mu.Unlock()
	now := time.Now()
	for _, pass := range passes {
		out := MapPass{
			Satellite: pass.Satellite, Receiver: pass.Receiver,
			Rise: pass.Rise, Set: pass.Set, MaxElevation: pass.MaxElevation,
			Active: now.After(pass.Rise) && now.Before(pass.Set),
		}
		if where, ok := coords[pass.Receiver]; ok {
			out.ReceiverLatitude, out.ReceiverLongitude = where[0], where[1]
		}
		data.Passes = append(data.Passes, out)
	}

	text, err := tle.Load("amateur", 24, false, 60*time.Second)
	if err != nil {
		// No TLE source (offline, upstream down): the map still gets
		// receivers and passes, and the UI says the satellite layer is
		// unavailable instead of failing the whole endpoint.
		return data
	}
	data.SatellitesAvailable = true
	for _, sat := range satellites.Table {
		if sat.Status != "active" {
			continue
		}
		tracker, err := tracking.FromTLEText(text, sat.Name)
		if err != nil {
			continue
		}
		// The observer coordinates are irrelevant for the sub-satellite
		// point; the prime-meridian equator is as good an origin as any.
		here, err := tracker.LookAngle(now, 0, 0, 0)
		if err != nil {
			continue
		}
		entry := MapSatellite{
			Name: sat.Name, NoradID: sat.NoradID, DownlinkHz: sat.DownlinkHz,
			Mode: sat.Mode, Latitude: here.Latitude, Longitude: here.Longitude,
			AltitudeKm: here.AltitudeKm, Track: [][2]float64{},
		}
		// Roughly one orbit around now: LEO periods are near 95 minutes.
		for offset := -50; offset <= 50; offset += 2 {
			point, err := tracker.LookAngle(now.Add(time.Duration(offset)*time.Minute), 0, 0, 0)
			if err != nil {
				break
			}
			entry.Track = append(entry.Track, [2]float64{point.Latitude, point.Longitude})
		}
		data.Satellites = append(data.Satellites, entry)
	}
	return data
}

// tilePattern matches /api/tiles/{z}/{x}/{y}.png exactly.
var tilePattern = regexp.MustCompile(`^/api/tiles/([0-9]+)/([0-9]+)/([0-9]+)\.png$`)

// tileCacheEntry is one cached tile.
type tileCacheEntry struct {
	body        []byte
	contentType string
	fetched     time.Time
}

// Tile cache is package level for the same reason as the map cache.
var (
	tilesMu sync.Mutex
	tiles   = map[string]tileCacheEntry{}
)

// tileUpstreams are tried in order; CARTO dark matches the UI theme, OSM is
// the fallback. Both allow modest proxying with a descriptive User-Agent.
// CARTO's free dark tiles moved behind an API key (they answer 200 with an
// "API key required" image), so plain OSM is the only upstream for now.
var tileUpstreams = []string{
	"https://tile.openstreetmap.org/%d/%d/%d.png",
}

// handleTile proxies one map tile through the same origin. Zoom and
// coordinates are range-checked before any upstream request, and responses
// are cached in memory (reset when the cap is hit, tiles are immutable).
func (s *Server) handleTile(w http.ResponseWriter, r *http.Request) {
	match := tilePattern.FindStringSubmatch(r.URL.Path)
	if match == nil {
		http.NotFound(w, r)
		return
	}
	z, _ := strconv.Atoi(match[1])
	x, _ := strconv.Atoi(match[2])
	y, _ := strconv.Atoi(match[3])
	if z < 0 || z > 19 || x < 0 || y < 0 || x >= 1<<z || y >= 1<<z {
		http.Error(w, "tile out of range", http.StatusBadRequest)
		return
	}
	key := fmt.Sprintf("%d/%d/%d", z, x, y)
	tilesMu.Lock()
	entry, ok := tiles[key]
	if ok && time.Since(entry.fetched) < 24*time.Hour {
		tilesMu.Unlock()
		w.Header().Set("Content-Type", entry.contentType)
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Write(entry.body)
		return
	}
	tilesMu.Unlock()

	client := &http.Client{Timeout: 10 * time.Second}
	var body []byte
	var contentType string
	for _, upstream := range tileUpstreams {
		request, err := http.NewRequestWithContext(r.Context(), "GET", fmt.Sprintf(upstream, z, x, y), nil)
		if err != nil {
			continue
		}
		request.Header.Set("User-Agent", "sdrstream tile proxy (https://github.com/Chaim12345/sdrstream)")
		response, err := client.Do(request)
		if err != nil {
			continue
		}
		if response.StatusCode != http.StatusOK {
			response.Body.Close()
			continue
		}
		body, err = io.ReadAll(io.LimitReader(response.Body, 1<<20))
		response.Body.Close()
		if err != nil {
			continue
		}
		contentType = response.Header.Get("Content-Type")
		break
	}
	if body == nil {
		http.Error(w, "tile unavailable", http.StatusBadGateway)
		return
	}
	if contentType == "" {
		contentType = "image/png"
	}
	tilesMu.Lock()
	if len(tiles) > 512 {
		tiles = map[string]tileCacheEntry{}
	}
	tiles[key] = tileCacheEntry{body: body, contentType: contentType, fetched: time.Now()}
	tilesMu.Unlock()
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write(body)
}
