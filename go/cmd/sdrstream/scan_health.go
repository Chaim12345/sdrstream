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
	// beaconReserveWindow is how long a beacon reservation keeps the
	// dispatcher from starting a new visit on the host. It must comfortably
	// cover a preflight plus a beaconListenMax measurement, and it is
	// refreshed on every retry while the current visit runs.
	beaconReserveWindow = 90 * time.Second
	// beaconReserveRetry is how soon the prober re-runs a pass while a
	// reservation is waiting for a visit to end.
	beaconReserveRetry = 20 * time.Second
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
	// visits marks hosts with a scan visit in flight (set at dispatch or
	// visit preflight, cleared at visit outcome); the beacon prober never
	// opens a second connection to a visited host.
	visits map[string]bool
	// beaconReserved holds health-table keys the dispatcher must leave
	// idle until the given time, so the beacon prober gets a quiet window
	// between visits. Without it a roster no bigger than the pool keeps
	// every member in a visit back-to-back and no measurement ever lands.
	beaconReserved map[string]time.Time
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
	return &healthPool{table: table, path: path, size: size, logf: logf, picks: map[string]bool{}, visits: map[string]bool{}, beaconReserved: map[string]time.Time{}}, nil
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

// Injectable for tests: the live defaults hit the network.
var (
	beaconPreflight = func(ctx context.Context, host string) kiwisdr.Status {
		return kiwiPreflight.Check(ctx, host)
	}
	beaconMeasure = kiwisdr.MeasureBeaconSNR
)

// beaconProber measures beacon SNR on Kiwi candidates, one short
// measurement per interval. A measurement only runs when the host has no
// scan visit in flight: public receivers limit channels per guest, and a
// second connection during a capture could disturb it. When every due
// candidate is a mid-visit pool member - the normal state of a roster no
// bigger than the pool, where visits run back-to-back - the prober
// reserves one host and retries shortly: the dispatcher leaves a reserved
// host idle after its visit, so the measurement gets a quiet window
// instead of never landing at all (QA: beacon_measured_at zero for every
// host over 45 minutes, with nothing logged).
func (p *healthPool) beaconProber(ctx context.Context, roster *scanRoster) {
	// Probe once early, then keep the polite cadence: a plain ticker would
	// fire only after a full beaconProbeInterval, and a run shorter than
	// the interval recorded nothing at all (QA: 15 min, zero measurements).
	timer := time.NewTimer(beaconProbeFirstDelay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			next := beaconProbeInterval
			if p.probeOneIdleBeacon(ctx, roster) {
				next = beaconReserveRetry
			}
			timer.Reset(next)
		}
	}
}

// probeOneIdleBeacon runs one prober pass and reports whether the prober
// should retry soon (a reservation is waiting for a visit to end). Every
// pass logs what it saw and why candidates were skipped: QA's
// zero-measurement runs were silent on every skip path, so it was
// impossible to tell whether the prober even ran.
func (p *healthPool) probeOneIdleBeacon(ctx context.Context, roster *scanRoster) (retrySoon bool) {
	if roster == nil {
		p.logf("scan health: beacon probe pass: no roster")
		return false
	}
	measured := map[string]time.Time{}
	for _, rec := range p.table.Snapshot() {
		measured[rec.Host] = rec.BeaconMeasuredAt
	}
	now := time.Now()
	plan := p.pickBeaconCandidates(roster.Snapshot(), measured, now)
	total := len(plan.candidates) + plan.fresh + len(plan.inVisit)
	if len(plan.candidates) == 0 {
		if len(plan.inVisit) > 0 {
			cand := plan.inVisit[0]
			p.reserveForBeacon(cand.key, now)
			p.logf("scan health: beacon probe pass: %d KiwiSDR roster entries: %d fresh, %d due but mid-visit; reserved %s for a measurement when its visit ends (retry in %s)",
				total, plan.fresh, len(plan.inVisit), cand.host, beaconReserveRetry)
			return true
		}
		p.logf("scan health: beacon probe pass: %d KiwiSDR roster entries: %d fresh, none due", total, plan.fresh)
		return false
	}
	for _, cand := range plan.candidates {
		if cand.picked {
			// A pool member can be re-dispatched at any moment. Reserve it
			// first so the dispatcher leaves it idle, then confirm no visit
			// started in between: the measurement must never be a second
			// connection during a capture.
			p.reserveForBeacon(cand.key, now)
			if p.visitInFlight(cand.key) {
				p.logf("scan health: beacon %s: a scan visit started before the reservation took; reserved, retry in %s", cand.host, beaconReserveRetry)
				return true
			}
		}
		// Preflight first (cached 15 min, daily budget per host): a busy or
		// dead receiver is not asked for a second connection.
		st := beaconPreflight(ctx, cand.host)
		p.table.ObserveProbe(cand.key, st.Latency, st.Free())
		if !st.Free() {
			p.logf("scan health: beacon %s skipped: preflight verdict %s: %s", cand.host, st.Verdict, strings.TrimSpace(st.Err))
			if cand.picked {
				p.releaseBeacon(cand.key)
			}
			continue
		}
		beacon := kiwisdr.DefaultBeacons[0]
		snr, err := beaconMeasure(ctx, cand.host, beacon, 0, kiwisdr.DefaultOptions())
		if cand.picked {
			p.releaseBeacon(cand.key)
		}
		if err != nil {
			p.logf("scan health: %s beacon %s: %v", cand.host, beacon.Name, err)
			break
		}
		p.table.ObserveBeaconSNR(cand.key, beacon.Name, snr)
		p.logf("scan health: %s beacon %s SNR %.1f dB (tone vs neighbouring floor)", cand.host, beacon.Name, snr)
		p.save()
		return false
	}
	// No measurement landed this pass. A permanently-unmeasurable
	// candidate (dead, busy, budget-spent) stays due forever, so
	// plan.candidates is never empty and the mid-visit reserve branch at
	// the top would never fire: every pass ended here, and the in-visit
	// good host never got its quiet window (QA: canary 10.255.255.1:8073
	// in the roster, "1 due candidate(s), none measurable" on every pass).
	// Reserve the first in-visit host here as well, so the retry measures
	// it once its visit ends.
	if len(plan.inVisit) > 0 {
		cand := plan.inVisit[0]
		p.reserveForBeacon(cand.key, now)
		p.logf("scan health: beacon probe pass: %d due candidate(s), none measurable; reserved %s for a measurement when its visit ends (retry in %s)",
			len(plan.candidates), cand.host, beaconReserveRetry)
		return true
	}
	p.logf("scan health: beacon probe pass: %d due candidate(s), none measurable (preflight skips above)", len(plan.candidates))
	return false
}

// reserveForBeacon keeps the dispatcher from starting a new visit on key
// for beaconReserveWindow. Prober passes refresh it while they wait.
func (p *healthPool) reserveForBeacon(key string, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.beaconReserved == nil {
		p.beaconReserved = map[string]time.Time{}
	}
	// Drop expired entries so the map cannot grow without bound.
	for k, until := range p.beaconReserved {
		if !now.Before(until) {
			delete(p.beaconReserved, k)
		}
	}
	p.beaconReserved[key] = now.Add(beaconReserveWindow)
}

// releaseBeacon lifts a reservation early, e.g. once the measurement is
// done and the host may scan again.
func (p *healthPool) releaseBeacon(key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.beaconReserved, key)
}

func (p *healthPool) visitInFlight(key string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.visits[key]
}

// tryStartVisit marks a scan visit in flight unless the host is reserved
// for a beacon measurement. The dispatcher calls it before starting the
// visit goroutine: the reservation check and the visit mark are one locked
// step, so a measurement and a capture can never overlap on one receiver.
func (p *healthPool) tryStartVisit(host string) bool {
	key := stationHostPortKey(host)
	p.mu.Lock()
	defer p.mu.Unlock()
	if until, ok := p.beaconReserved[key]; ok && time.Now().Before(until) {
		return false
	}
	p.visits[key] = true
	return true
}

// beaconCandidate is one receiver due for a beacon SNR measurement.
type beaconCandidate struct {
	host   string // roster host as dialled (may carry scheme/port)
	key    string // health-table key (stationHostPortKey)
	picked bool   // pool member; measuring it needs a reservation
}

// beaconPassPlan is one prober pass's view of the KiwiSDR roster entries.
type beaconPassPlan struct {
	// candidates are due and have no visit in flight: idle entries (not in
	// the pool) first, then pool members between visits.
	candidates []beaconCandidate
	// fresh counts entries skipped because their beacon reading is fresh.
	fresh int
	// inVisit holds due entries a scan visit is using right now; the
	// prober reserves one of them when nothing else is measurable.
	inVisit []beaconCandidate
}

// pickBeaconCandidates orders KiwiSDR roster entries whose beacon reading
// is missing or stale, best first. Pool members in a visit are never
// measured: a second connection could disturb the capture, and public
// receivers limit channels per guest. The picked-but-idle class is what
// keeps a small roster measurable: when the pool covers every KiwiSDR
// entry, skipping picks wholesale left beacon_measured_at zero forever.
func (p *healthPool) pickBeaconCandidates(entries []string, measured map[string]time.Time, now time.Time) beaconPassPlan {
	picks := p.activePicks()
	p.mu.Lock()
	inVisit := make(map[string]bool, len(p.visits))
	for key := range p.visits {
		inVisit[key] = true
	}
	p.mu.Unlock()
	var plan beaconPassPlan
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
			plan.fresh++
			continue
		}
		cand := beaconCandidate{host: host, key: key, picked: picks[key]}
		switch {
		case inVisit[key]:
			plan.inVisit = append(plan.inVisit, cand)
		case !cand.picked:
			idle = append(idle, cand)
		default:
			pickedIdle = append(pickedIdle, cand)
		}
	}
	plan.candidates = append(idle, pickedIdle...)
	return plan
}
