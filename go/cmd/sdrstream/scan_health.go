package main

// Health-driven scan pool. The wide scanner ramped across the whole roster
// holding a dozen receiver connections and captured nothing; this pool
// holds 1-3 connections on the healthiest receivers (probe latency, beacon
// SNR on a known station, last successful capture), rotates them so no
// receiver is pinned for hours, and persists the table across restarts.

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/kiwisdr"
)

const (
	// healthRepickInterval is the rotation cadence: roughly one station
	// session, so each receiver gets a visit and then rests.
	healthRepickInterval = 5 * time.Minute
	// beaconProbeInterval is the most often the prober measures one idle
	// candidate; one 5s connection per interval is the polite ceiling.
	beaconProbeInterval = 10 * time.Minute
	// beaconProbeFirstDelay is when the first probe runs after startup. A
	// plain ticker would wait a whole beaconProbeInterval first, so a run
	// shorter than the interval never recorded a beacon measurement.
	beaconProbeFirstDelay = time.Minute
	// beaconSNRStale is how long a beacon SNR reading stays fresh.
	beaconSNRStale = 6 * time.Hour
	// healthSaveInterval is how often the table is persisted while running.
	healthSaveInterval = 2 * time.Minute
)

// healthPool picks and tracks the rotating scan pool. scanHealth is nil in
// wide mode; it is set once in cmdWeb before the scanner starts and never
// reassigned, so scan goroutines read it without a lock.
type healthPool struct {
	table *kiwisdr.HealthTable
	path  string
	size  int
	logf  func(string, ...any)

	mu     sync.Mutex
	picks  map[string]bool
	repick time.Time
	// visits marks hosts with a scan visit in flight (set at visit
	// preflight, cleared at visit outcome); the beacon prober skips them.
	visits map[string]bool
}

var scanHealth *healthPool

// healthTablePath follows the blocklist convention: SDRSTREAM_STATE_DIR or
// a per-user state directory.
func healthTablePath() string {
	dir := os.Getenv("SDRSTREAM_STATE_DIR")
	if dir == "" {
		dir = filepath.Join(os.Getenv("HOME"), "sdrstream-state")
	}
	return filepath.Join(dir, "receiver-health.json")
}

func newHealthPool(size int, path string, logf func(string, ...any)) (*healthPool, error) {
	if size < kiwisdr.HealthPoolMin || size > kiwisdr.HealthPoolMax {
		return nil, fmt.Errorf("--scan-pool-size must be %d..%d", kiwisdr.HealthPoolMin, kiwisdr.HealthPoolMax)
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	table, err := kiwisdr.LoadHealth(path)
	if err != nil {
		return nil, fmt.Errorf("load receiver health table: %w", err)
	}
	if n := len(table.Snapshot()); n > 0 {
		logf("scan health: loaded %d receiver record(s) from %s", n, path)
	}
	return &healthPool{table: table, path: path, size: size, logf: logf, picks: map[string]bool{}, visits: map[string]bool{}}, nil
}

// pickStations restricts the deduped roster to the pool's current picks and
// records the next rotation time. Roster entries are "Type|host"; the table
// is keyed by stationHostPortKey so every observation path agrees.
func (p *healthPool) pickStations(unique []string) []string {
	hosts := make([]string, 0, len(unique))
	byHost := map[string]string{}
	for _, entry := range unique {
		parts := strings.SplitN(entry, "|", 2)
		if len(parts) != 2 {
			continue
		}
		key := stationHostPortKey(parts[1])
		hosts = append(hosts, key)
		byHost[key] = entry
	}
	chosen := p.table.Pick(hosts, p.size)
	p.mu.Lock()
	p.picks = map[string]bool{}
	for _, h := range chosen {
		p.picks[h] = true
	}
	p.repick = time.Now().Add(healthRepickInterval)
	p.mu.Unlock()
	out := make([]string, 0, len(chosen))
	for _, h := range chosen {
		if entry, ok := byHost[h]; ok {
			out = append(out, entry)
		}
	}
	p.logf("scan health: pool %d/%d: %s", len(out), p.size, strings.Join(chosen, ", "))
	return out
}

// repickDue reports whether the rotation interval has elapsed.
func (p *healthPool) repickDue() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return time.Now().After(p.repick)
}

// activePicks returns the current pool members (station host keys).
func (p *healthPool) activePicks() map[string]bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return maps.Clone(p.picks)
}

// observeProbe folds a preflight result into the table and marks the
// host's scan visit as in flight, so the beacon prober leaves it alone.
func (p *healthPool) observeProbe(host string, st kiwisdr.Status) {
	key := stationHostPortKey(host)
	p.table.ObserveProbe(key, st.Latency, st.Free())
	p.mu.Lock()
	p.visits[key] = true
	p.mu.Unlock()
}

// observeOutcome folds one finished station visit into the table. Skips and
// local-resource failures say nothing about the receiver: no observation.
// The visit mark is always cleared: the connection is over either way.
func (p *healthPool) observeOutcome(host string, o scanOutcome) {
	key := stationHostPortKey(host)
	p.mu.Lock()
	delete(p.visits, key)
	p.mu.Unlock()
	switch {
	case o.skipped, o.localResource:
		return
	case o.refused || o.timedOut:
		p.table.ObserveCapture(key, false)
	default:
		p.table.ObserveCapture(key, true)
	}
}

// save persists the table; called periodically, after beacon measurements,
// and on shutdown.
func (p *healthPool) save() {
	if p.path == "" {
		return
	}
	if err := p.table.SaveHealth(p.path); err != nil {
		p.logf("scan health: save %s: %v", p.path, err)
	}
}

func (p *healthPool) saveLoop(ctx context.Context) {
	ticker := time.NewTicker(healthSaveInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.save()
		}
	}
}

// beaconProber measures beacon SNR on Kiwi candidates the pool is not
// currently using, one short measurement per interval. Idle candidates only:
// a receiver already in the pool is serving a scan connection, and public
// receivers allow one connection per guest.
func (p *healthPool) beaconProber(ctx context.Context, roster *scanRoster) {
	// Probe once early, then keep the polite cadence: a plain ticker would
	// fire only after a full beaconProbeInterval, and a run shorter than
	// the interval recorded nothing at all (QA: 15 min, zero measurements).
	first := time.NewTimer(beaconProbeFirstDelay)
	defer first.Stop()
	ticker := time.NewTicker(beaconProbeInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-first.C:
			p.probeOneIdleBeacon(ctx, roster)
		case <-ticker.C:
			p.probeOneIdleBeacon(ctx, roster)
		}
	}
}

func (p *healthPool) probeOneIdleBeacon(ctx context.Context, roster *scanRoster) {
	if roster == nil {
		return
	}
	measured := map[string]time.Time{}
	for _, rec := range p.table.Snapshot() {
		measured[rec.Host] = rec.BeaconMeasuredAt
	}
	for _, cand := range p.pickBeaconCandidates(roster.Snapshot(), measured, time.Now()) {
		// Preflight first (cached 15 min, daily budget per host): a busy or
		// dead receiver is not asked for a second connection.
		st := kiwiPreflight.Check(ctx, cand.host)
		p.table.ObserveProbe(cand.key, st.Latency, st.Free())
		if !st.Free() {
			continue
		}
		beacon := kiwisdr.DefaultBeacons[0]
		snr, err := kiwisdr.MeasureBeaconSNR(ctx, cand.host, beacon, 0, kiwisdr.DefaultOptions())
		if err != nil {
			p.logf("scan health: %s beacon %s: %v", cand.host, beacon.Name, err)
			return
		}
		p.table.ObserveBeaconSNR(cand.key, beacon.Name, snr)
		p.logf("scan health: %s beacon %s SNR %.1f dB (tone vs neighbouring floor)", cand.host, beacon.Name, snr)
		p.save()
		return
	}
}

// beaconCandidate is one receiver due for a beacon SNR measurement.
type beaconCandidate struct {
	host string // roster host as dialled (may carry scheme/port)
	key  string // health-table key (stationHostPortKey)
}

// pickBeaconCandidates orders KiwiSDR roster entries whose beacon reading
// is missing or stale, best first: idle candidates (not in the pool) come
// before pool members whose scan visit is not in flight. Pool members in a
// visit are left alone: a second connection could disturb the capture, and
// public receivers limit channels per guest. The picked-but-idle class is
// what keeps a small roster measurable: when the pool covers every KiwiSDR
// entry, skipping picks wholesale left beacon_measured_at zero forever.
func (p *healthPool) pickBeaconCandidates(entries []string, measured map[string]time.Time, now time.Time) []beaconCandidate {
	picks := p.activePicks()
	p.mu.Lock()
	inVisit := make(map[string]bool, len(p.visits))
	for key := range p.visits {
		inVisit[key] = true
	}
	p.mu.Unlock()
	var idle, pickedIdle []beaconCandidate
	for _, entry := range entries {
		parts := strings.SplitN(entry, "|", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "KiwiSDR") {
			continue
		}
		host := strings.TrimSpace(parts[1])
		if host == "" {
			continue
		}
		key := stationHostPortKey(host)
		if at, ok := measured[key]; ok && now.Sub(at) < beaconSNRStale {
			continue
		}
		cand := beaconCandidate{host: host, key: key}
		switch {
		case !picks[key]:
			idle = append(idle, cand)
		case !inVisit[key]:
			pickedIdle = append(pickedIdle, cand)
		}
	}
	return append(idle, pickedIdle...)
}
