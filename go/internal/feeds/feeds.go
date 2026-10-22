// Package feeds collects live, keyless public data feeds (ADS-B aircraft,
// APRS-IS, PSK Reporter digimode spots, Reverse Beacon Network and AIS via
// aisstream.io) into one ring buffer for the web UI. Every item is real data
// received from the named source and is labelled with that source and its
// time; the package never fabricates entries.
package feeds

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"sort"
	"sync"
	"time"
)

// Item is one live data point from a public feed.
type Item struct {
	ID     string    `json:"id"`
	Source string    `json:"source"` // adsb.lol, aprs-is, pskreporter, rbn, aisstream
	Kind   string    `json:"kind"`   // aircraft, aprs, digimode, beacon, ais
	Time   time.Time `json:"time"`
	Label  string    `json:"label"`
	Detail string    `json:"detail,omitempty"`
	Lat    float64   `json:"lat,omitempty"`
	Lon    float64   `json:"lon,omitempty"`
	HasPos bool      `json:"has_pos,omitempty"`
	Ref    string    `json:"ref,omitempty"` // upstream link for the item
}

// StreamSource is a long-lived feed connection (TCP, WebSocket, MQTT).
type StreamSource interface {
	Name() string
	Kind() string
	// Run streams items until ctx is cancelled; a returned error triggers a
	// reconnect with backoff.
	Run(ctx context.Context, emit func(Item)) error
}

// Readier is a streaming source that can also report when its subscription
// is established, so a quiet feed shows as live before its first item.
type Readier interface {
	StreamSource
	RunReady(ctx context.Context, emit func(Item), ready func()) error
}

// Poller is a feed queried on an interval (REST).
type Poller interface {
	Name() string
	Kind() string
	Poll(ctx context.Context) ([]Item, error)
	Interval() time.Duration
}

// SourceStatus reports one feed's health for the UI.
type SourceStatus struct {
	Name           string    `json:"name"`
	Kind           string    `json:"kind"`
	State          string    `json:"state"` // live, connecting, error, disabled
	Detail         string    `json:"detail,omitempty"`
	Items          int       `json:"items"`
	LastOK         time.Time `json:"last_ok,omitempty"`
	LastError      string    `json:"last_error,omitempty"`
	RefreshSeconds int       `json:"refresh_seconds,omitempty"`
	Samples        []Item    `json:"samples,omitempty"`
	DetailURL      string    `json:"detail_url,omitempty"`
	Attribution    string    `json:"attribution,omitempty"`
}

// Snapshot is the API payload for /api/feeds.json.
type Snapshot struct {
	UpdatedAt time.Time      `json:"updated_at"`
	Sources   []SourceStatus `json:"sources"`
	Items     []Item         `json:"items"`
}

type sourceState struct {
	status   SourceStatus
	maxAge   time.Duration
	disabled bool
}

// Manager aggregates every feed into one bounded buffer.
type Manager struct {
	mu      sync.Mutex
	items   map[string]*Item
	sources map[string]*sourceState
	cap     int
	logf    func(string, ...any)
}

// New builds a Manager keeping at most capacity items (default 2000).
func New(capacity int, logf func(string, ...any)) *Manager {
	if capacity <= 0 {
		capacity = 2000
	}
	if logf == nil {
		logf = func(f string, a ...any) { log.Printf("feeds: "+f, a...) }
	}
	return &Manager{items: map[string]*Item{}, sources: map[string]*sourceState{}, cap: capacity, logf: logf}
}

func (m *Manager) source(name, kind string, maxAge time.Duration) *sourceState {
	st, ok := m.sources[name]
	if !ok {
		st = &sourceState{status: SourceStatus{Name: name, Kind: kind, State: "connecting"}, maxAge: maxAge}
		if name == "sondehub" {
			st.status.Attribution = "SondeHub contributors · CC BY-SA 2.0 · latest per sonde"
			st.status.DetailURL = "https://sondehub.org/"
			st.status.Detail = "One bootstrap fetch, then live MQTT/WebSocket updates"
		}
		m.sources[name] = st
	}
	return st
}

// Disable registers a source that cannot run yet (for example a missing API
// key) so the UI can explain what is needed.
func (m *Manager) Disable(name, kind, detail string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.source(name, kind, 0)
	st.disabled = true
	st.status.State = "disabled"
	st.status.Detail = detail
}

func (m *Manager) setState(name, state, errText string) {
	st := m.sources[name]
	st.status.State = state
	if errText != "" {
		st.status.LastError = errText
	}
	if state == "live" {
		st.status.LastOK = time.Now()
		st.status.LastError = ""
	}
}

// Add inserts or refreshes items, keyed by ID so repeated sightings update
// in place instead of flooding the list.
func (m *Manager) Add(items ...Item) {
	if len(items) == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.addLocked(items)
}

func countFor(items map[string]*Item, source string) int {
	n := 0
	for _, it := range items {
		if it.Source == source {
			n++
		}
	}
	return n
}

// evictLocked drops the n oldest items.
func (m *Manager) evictLocked(n int) {
	all := make([]*Item, 0, len(m.items))
	for _, it := range m.items {
		all = append(all, it)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Time.Before(all[j].Time) })
	for i := 0; i < n && i < len(all); i++ {
		delete(m.items, all[i].ID)
	}
}

// prune drops items past their source's max age (ADS-B positions go stale
// in about a minute; digimode spots stay longer).
func (m *Manager) prune(now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, it := range m.items {
		st := m.sources[it.Source]
		if st != nil && st.maxAge > 0 && now.Sub(it.Time) > st.maxAge {
			delete(m.items, id)
		}
	}
	for _, st := range m.sources {
		st.status.Items = countFor(m.items, st.status.Name)
	}
}

// Snapshot returns a consistent view: sources sorted by name, items newest
// first, capped at maxItems (0 = 500).
func (m *Manager) Snapshot(maxItems int) Snapshot {
	if maxItems <= 0 {
		maxItems = 500
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	snap := Snapshot{UpdatedAt: time.Now()}
	for _, st := range m.sources {
		status := st.status
		for _, it := range m.items {
			if it.Source == status.Name {
				status.Samples = append(status.Samples, *it)
			}
		}
		sort.Slice(status.Samples, func(i, j int) bool { return status.Samples[i].Time.After(status.Samples[j].Time) })
		if len(status.Samples) > 4 {
			status.Samples = status.Samples[:4]
		}
		snap.Sources = append(snap.Sources, status)
	}
	sort.Slice(snap.Sources, func(i, j int) bool { return snap.Sources[i].Name < snap.Sources[j].Name })
	items := make([]Item, 0, len(m.items))
	for _, it := range m.items {
		items = append(items, *it)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Time.After(items[j].Time) })
	if len(items) > maxItems {
		items = items[:maxItems]
	}
	snap.Items = items
	return snap
}

// FindByLabel returns recent items whose label is exactly call (case
// insensitive) within the window - used to cross-check a decode against
// independent public spot networks.
func (m *Manager) FindByLabel(call string, within time.Duration) []Item {
	m.mu.Lock()
	defer m.mu.Unlock()
	cutoff := time.Now().Add(-within)
	var out []Item
	for _, it := range m.items {
		if equalFoldASCII(it.Label, call) && it.Time.After(cutoff) {
			out = append(out, *it)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
	return out
}

func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'a' <= ca && ca <= 'z' {
			ca -= 32
		}
		if 'a' <= cb && cb <= 'z' {
			cb -= 32
		}
		if ca != cb {
			return false
		}
	}
	return true
}

// RunStream supervises one streaming source: connect, stream, and reconnect
// with backoff (5s doubling to 5m) on failure, forever until ctx ends.
func (m *Manager) RunStream(ctx context.Context, s StreamSource, maxAge time.Duration) {
	m.mu.Lock()
	m.source(s.Name(), s.Kind(), maxAge)
	m.mu.Unlock()
	backoff := streamBackoffBase
	for {
		if ctx.Err() != nil {
			return
		}
		attemptStart := time.Now()
		m.mu.Lock()
		m.setState(s.Name(), "connecting", "")
		m.mu.Unlock()
		emit := func(it Item) {
			if it.Source == "" {
				it.Source = s.Name()
			}
			if it.Kind == "" {
				it.Kind = s.Kind()
			}
			m.Add(it)
			m.mu.Lock()
			m.setState(s.Name(), "live", "")
			m.mu.Unlock()
		}
		var err error
		if r, ok := s.(Readier); ok {
			err = r.RunReady(ctx, emit, func() {
				m.mu.Lock()
				m.setState(s.Name(), "live", "")
				m.mu.Unlock()
			})
		} else {
			err = s.Run(ctx, emit)
		}
		if ctx.Err() != nil {
			return
		}
		// A run that stayed up for a minute or more was healthy: the
		// failure is a fresh drop, so restart the backoff instead of
		// punishing the feed with the stale maximum.
		if time.Since(attemptStart) >= healthyRunThreshold {
			backoff = streamBackoffBase
		}
		// Jitter (0.75x-1.25x) keeps several feeds from reconnecting in
		// lockstep after a shared outage.
		delay := feedBackoffWithJitter(backoff)
		m.logf("%s: %v; reconnecting in %s", s.Name(), err, delay)
		m.mu.Lock()
		m.setState(s.Name(), "error", errString(err))
		m.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		if backoff < 5*time.Minute {
			backoff *= 2
		}
	}
}

// streamBackoffBase is the first reconnect delay; it doubles to 5m. A var
// so tests can shorten it.
var streamBackoffBase = 5 * time.Second

// healthyRunThreshold is how long a streaming connection must stay up for
// its eventual drop to count as a fresh failure (backoff resets) rather
// than a continuation of connect instability. A var so tests can shorten it.
var healthyRunThreshold = time.Minute

// feedBackoffWithJitter spreads reconnects: base * U[0.75, 1.25).
func feedBackoffWithJitter(base time.Duration) time.Duration {
	return base*3/4 + time.Duration(rand.Float64()*float64(base)/2)
}

// RunPoller supervises one polled source on its own interval.
func (m *Manager) RunPoller(ctx context.Context, p Poller, maxAge time.Duration) {
	m.mu.Lock()
	st := m.source(p.Name(), p.Kind(), maxAge)
	st.status.RefreshSeconds = int(p.Interval() / time.Second)
	switch p.Name() {
	case "noaa-swpc":
		st.status.Attribution = "NOAA SWPC · US government data"
		st.status.DetailURL = "https://www.swpc.noaa.gov/"
	case "wspr.live":
		st.status.Attribution = "wspr.live / WSPR contributors · public query results"
		st.status.DetailURL = "https://wspr.live/"
	case "sondehub":
		st.status.Attribution = "SondeHub contributors · CC BY-SA 2.0 · latest per sonde"
		st.status.DetailURL = "https://sondehub.org/"
	case "digitraffic":
		st.status.Attribution = "Fintraffic / Digitraffic · CC BY 4.0 · Baltic coverage"
		st.status.DetailURL = "https://www.digitraffic.fi/en/marine-traffic/"
	case "adsb.lol":
		st.status.Attribution = "adsb.lol contributors · ODbL"
		st.status.DetailURL = "https://adsb.lol/"
		if a, ok := p.(*ADSB); ok {
			st.status.Detail = fmt.Sprintf("Centre %.4f, %.4f · %.0f nm · configure --feeds-lat / --feeds-lon", a.Lat, a.Lon, a.Radius)
		}
	}
	m.mu.Unlock()
	poll := func() {
		items, err := p.Poll(ctx)
		m.mu.Lock()
		defer m.mu.Unlock()
		if err != nil {
			if ctx.Err() == nil {
				m.logf("%s: %v", p.Name(), err)
				m.setState(p.Name(), "error", errString(err))
			}
			return
		}
		for i := range items {
			if items[i].Source == "" {
				items[i].Source = p.Name()
			}
			if items[i].Kind == "" {
				items[i].Kind = p.Kind()
			}
		}
		// Successful polls replace this source cache. Failures keep the last good
		// observations, visibly marked as error, until normal age pruning.
		for id, it := range m.items {
			if it.Source == p.Name() {
				delete(m.items, id)
			}
		}
		m.setState(p.Name(), "live", "")
		m.mu2Add(items)
		m.sources[p.Name()].status.Items = countFor(m.items, p.Name())
	}
	poll()
	tick := time.NewTicker(p.Interval())
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			poll()
		}
	}
}

// mu2Add is Add called with m.mu already held.
func (m *Manager) mu2Add(items []Item) { m.addLocked(items) }

func (m *Manager) addLocked(items []Item) {
	affected := map[string]bool{}
	for _, it := range items {
		if it.ID == "" || it.Label == "" {
			continue
		}
		if it.Time.IsZero() {
			it.Time = time.Now()
		}
		if old, ok := m.items[it.ID]; ok {
			*old = it
		} else {
			copy := it
			m.items[it.ID] = &copy
		}
		affected[it.Source] = true
	}
	for name := range affected {
		st, ok := m.sources[name]
		if !ok {
			st = m.source(name, "", 0)
		}
		st.status.Items = countFor(m.items, name)
	}
	if len(m.items) > m.cap+m.cap/4 {
		m.evictLocked(len(m.items) - m.cap)
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// PruneLoop removes stale items until ctx ends; run it once per Manager.
func (m *Manager) PruneLoop(ctx context.Context) {
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			m.prune(now)
		}
	}
}
