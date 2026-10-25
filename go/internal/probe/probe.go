// Package probe keeps a persistent catalog of what every publicly listed
// SDR receiver can receive. A scheduler sweeps the public directories
// (receiverbook for OpenWebRX/WebSDR, the KiwiSDR listing), probes each
// station for liveness and capability, and records the result. The
// catalog is the data layer the decoder fleet and the web UI build on:
// it answers "which stations exist, are they alive, and what can they
// hear" without each consumer probing for itself.
//
// Politeness: one probe per station per sweep, bounded concurrency, and
// failures only increment counters - a dead station costs one request
// per sweep, never more.
package probe

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/common"
)

// Kind identifies the receiver protocol family.
const (
	KindKiwi      = "kiwi"
	KindOpenWebRX = "openwebrx"
	KindWebSDR    = "websdr"
)

// Liveness verdicts, mirroring the kiwisdr preflight classes.
const (
	VerdictOK      = "ok"
	VerdictBusy    = "busy"    // alive, no free slot
	VerdictDead    = "dead"    // HTTP error or malformed answer
	VerdictTimeout = "timeout" // nothing answered in time
)

// Capability is what a station can receive, from the directory listing and
// the probe answer. Every field is optional: WebSDR stations expose no
// machine-readable capability, so only liveness is recorded for them.
type Capability struct {
	FreqMinHz  int64    `json:"freq_min_hz,omitempty"`
	FreqMaxHz  int64    `json:"freq_max_hz,omitempty"`
	Profiles   []string `json:"profiles,omitempty"`    // OpenWebRX SDR profiles
	UsersMax   int      `json:"users_max,omitempty"`   // KiwiSDR advertised slots
	RXChannels int      `json:"rx_channels,omitempty"` // KiwiSDR hardware channels
	Antenna    string   `json:"antenna,omitempty"`     // listing-reported antenna
	Location   string   `json:"location,omitempty"`    // listing-reported location
	Version    string   `json:"version,omitempty"`     // server software version
	Passworded bool     `json:"passworded,omitempty"`  // KiwiSDR requires a password
}

// Record is one station in the catalog.
type Record struct {
	Key  string  `json:"key"` // normalized identity: host:port or trimmed URL
	Kind string  `json:"kind"`
	Name string  `json:"name"`
	URL  string  `json:"url"`
	Lat  float64 `json:"lat,omitempty"`
	Lon  float64 `json:"lon,omitempty"`

	// Listing facts, refreshed from the directory each sweep.
	ListedOffline bool   `json:"listed_offline,omitempty"`
	ListedUpdated string `json:"listed_updated,omitempty"`

	// Probe outcome from the most recent sweep.
	Reachable      bool          `json:"reachable"`
	Verdict        string        `json:"verdict"`
	Latency        time.Duration `json:"latency,omitempty"`
	CheckedAt      time.Time     `json:"checked_at"`
	ConsecFailures int           `json:"consec_failures,omitempty"`
	LastError      string        `json:"last_error,omitempty"`

	// NextCheckAt is when the monitor should probe this station again:
	// base interval for healthy stations, doubling backoff (capped at 24h)
	// for consecutive failures so dead receivers are not hammered.
	// Zero means due immediately (new stations).
	NextCheckAt time.Time `json:"next_check_at,omitempty"`

	// Capability: last KNOWN answer. A failed probe never erases it -
	// Reachable=false is what marks the station down, so a transient
	// outage cannot blank the catalog the UI shows.
	Capability Capability `json:"capability,omitempty"`
}

// Event kinds for the catalog's health-transition ring.
const (
	EventRecovered   = "recovered"   // unreachable -> reachable
	EventUnreachable = "unreachable" // reachable -> unreachable
)

// Event is one observed health transition. Events are monitoring facts
// derived from probe outcomes - they say a station stopped or resumed
// answering probes, never anything about RF reception.
type Event struct {
	Key     string    `json:"key"`
	At      time.Time `json:"at"`
	Kind    string    `json:"kind"`              // EventRecovered or EventUnreachable
	Verdict string    `json:"verdict,omitempty"` // why, for EventUnreachable
}

// maxEvents bounds the catalog's transition ring; oldest events drop off.
const maxEvents = 200

// Catalog is the persisted station catalog. It is safe for concurrent
// use: the sweep scheduler writes while HTTP handlers read, and an
// unlocked concurrent map read during a write panics fatally (QA review).
type Catalog struct {
	Version   int                `json:"version"`
	UpdatedAt time.Time          `json:"updated_at"`
	Records   map[string]*Record `json:"records"`
	// Events is a bounded ring (maxEvents) of reachability transitions,
	// oldest first. A station appearing in the directory is not an
	// event; only observed reachable<->unreachable flips are.
	Events []Event `json:"events,omitempty"`

	mu sync.RWMutex
}

// catalogVersion is bumped when the on-disk shape changes.
const catalogVersion = 1

// NewCatalog returns an empty catalog.
func NewCatalog() *Catalog {
	return &Catalog{Version: catalogVersion, Records: map[string]*Record{}}
}

// Touch stamps the catalog's update time.
func (c *Catalog) Touch(t time.Time) {
	c.mu.Lock()
	c.UpdatedAt = t
	c.mu.Unlock()
}

// Snapshot is a lock-free copy of the catalog for readers.
type Snapshot struct {
	Version   int
	UpdatedAt time.Time
	Records   map[string]*Record
	Events    []Event
}

// Snapshot returns a copy of the catalog for readers (HTTP handlers) that
// must not hold the lock while serializing. Records and their capability
// slices are copied, so the caller can range freely mid-sweep. The copy
// deliberately drops the mutex (vet: no lock copying).
func (c *Catalog) Snapshot() Snapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := Snapshot{
		Version:   c.Version,
		UpdatedAt: c.UpdatedAt,
		Records:   make(map[string]*Record, len(c.Records)),
		Events:    append([]Event(nil), c.Events...),
	}
	for k, r := range c.Records {
		cp := *r
		cp.Capability.Profiles = append([]string(nil), r.Capability.Profiles...)
		out.Records[k] = &cp
	}
	return out
}

// Load reads the catalog from path. A missing file is an empty catalog,
// not an error. A version mismatch or corrupt file is an error: silently
// loading a wrong-shaped catalog would mislead the UI.
func Load(path string) (*Catalog, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return NewCatalog(), nil
	}
	if err != nil {
		return nil, err
	}
	var c Catalog
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("probe catalog %s: %w", path, err)
	}
	if c.Version != catalogVersion {
		return nil, fmt.Errorf("probe catalog %s: version %d, want %d", path, c.Version, catalogVersion)
	}
	if c.Records == nil {
		c.Records = map[string]*Record{}
	}
	return &c, nil
}

// Save writes the catalog atomically (temp + rename, 0600) so a reader
// never sees a half-written file.
func (c *Catalog) Save(path string) error {
	c.mu.RLock()
	data, err := json.MarshalIndent(c, "", "  ")
	c.mu.RUnlock()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return common.WriteFileAtomic(path, data, 0o600)
}

// Update merges one probe outcome into the catalog keyed by rec.Key.
// Identity and listing fields are always refreshed; capability is kept
// from the previous record when the probe did not answer (so one bad
// sweep does not blank what the station can receive).
func (c *Catalog) Update(rec Record) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Records == nil {
		c.Records = map[string]*Record{}
	}
	if prev, ok := c.Records[rec.Key]; ok {
		if !rec.Reachable {
			rec.Capability = prev.Capability
			rec.ConsecFailures = prev.ConsecFailures + 1
		}
		if prev.Reachable != rec.Reachable {
			kind := EventRecovered
			if prev.Reachable {
				kind = EventUnreachable
			}
			c.Events = append(c.Events, Event{
				Key: rec.Key, At: rec.CheckedAt, Kind: kind, Verdict: rec.Verdict,
			})
			if len(c.Events) > maxEvents {
				c.Events = c.Events[len(c.Events)-maxEvents:]
			}
		}
	}
	r := rec
	c.Records[rec.Key] = &r
}

// Get returns a copy of one record, for the scheduler's backoff decision.
func (c *Catalog) Get(key string) (Record, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	r, ok := c.Records[key]
	if !ok {
		return Record{}, false
	}
	return *r, true
}

// Backoff returns how long to wait before re-checking a station after
// consecFailures consecutive failed probes: the base interval after the
// first failure, doubling with each further failure, capped at 24h.
// Healthy stations are scheduled with the base interval directly.
func Backoff(base time.Duration, consecFailures int) time.Duration {
	const cap = 24 * time.Hour
	if consecFailures <= 1 {
		return base
	}
	d := base
	for i := 1; i < consecFailures; i++ {
		d *= 2
		if d >= cap {
			return cap
		}
	}
	return d
}

// Reachable returns the records that answered their last probe, oldest
// check first - the order a fleet scheduler should reconsider them.
func (c *Catalog) Reachable() []*Record {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]*Record, 0, len(c.Records))
	for _, r := range c.Records {
		if r.Reachable {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CheckedAt.Before(out[j].CheckedAt) })
	return out
}
