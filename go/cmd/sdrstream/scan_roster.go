package main

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/decode"
)

// scanRoster keeps the manual station list and the directory-derived list
// separately. A refresh can replace the dynamic half without losing a
// hand-picked receiver, and a failed refresh keeps the previous list.
type scanRoster struct {
	mu      sync.Mutex
	manual  []string
	dynamic []string

	refreshMu   sync.Mutex
	refresh     func(context.Context) ([]string, error)
	interval    time.Duration
	lastRefresh time.Time
	logf        func(string, ...interface{})
}

func newScanRoster(logf func(string, ...interface{})) *scanRoster {
	if logf == nil {
		logf = func(string, ...interface{}) {}
	}
	return &scanRoster{logf: logf}
}

// AddManual records hand-picked stations. Every entry must name the
// receiver kind: "KiwiSDR|host:8073", "OpenWebRX|http://host:8073/", ... A
// bare host:port used to be stored and then silently dropped by Snapshot,
// which split on "|" and skipped entries without a kind; now it is rejected
// here, loudly, so the operator sees the mistake at startup instead of
// wondering why a receiver never scans. An empty kind ("|host") stays
// allowed - scanStation guesses the type from the host name - but that is
// logged too, because a wrong guess drives the receiver with the wrong
// protocol.
func (r *scanRoster) AddManual(stations ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, station := range stations {
		station = strings.TrimSpace(station)
		if station == "" {
			continue
		}
		kind, host, found := strings.Cut(station, "|")
		if !found {
			r.logf("scan: --scan-stations entry %q has no Kind| prefix and will never scan; dropping it (use e.g. KiwiSDR|%[1]s; kinds: KiwiSDR, OpenWebRX, WebSDR)", station)
			continue
		}
		kind, host = strings.TrimSpace(kind), strings.TrimSpace(host)
		if host == "" {
			r.logf("scan: --scan-stations entry %q has an empty host after the Kind| prefix; dropping it", station)
			continue
		}
		if kind == "" {
			r.logf("scan: --scan-stations entry %q has an empty Kind; the scanner will guess the receiver type from the host name", station)
		}
		r.manual = append(r.manual, kind+"|"+host)
	}
}

func (r *scanRoster) SetDynamic(stations []string) {
	clean := make([]string, 0, len(stations))
	for _, station := range stations {
		station = strings.TrimSpace(station)
		if station != "" {
			clean = append(clean, station)
		}
	}
	r.mu.Lock()
	r.dynamic = clean
	r.mu.Unlock()
}

// Snapshot merges manual and dynamic entries, deduped by host:port. The
// scanner may hold a snapshot for a whole pass; a refresh changes the next
// pass, not the one in flight.
func (r *scanRoster) Snapshot() []string {
	r.mu.Lock()
	manual := append([]string(nil), r.manual...)
	dynamic := append([]string(nil), r.dynamic...)
	r.mu.Unlock()
	out := make([]string, 0, len(manual)+len(dynamic))
	seen := map[string]bool{}
	for _, station := range append(manual, dynamic...) {
		parts := strings.SplitN(strings.TrimSpace(station), "|", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[1]) == "" {
			continue
		}
		key := stationHostPortKey(parts[1])
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, strings.TrimSpace(parts[0])+"|"+strings.TrimSpace(parts[1]))
	}
	return out
}

// Refresh fetches the directory list once and replaces the dynamic half.
// A failed or empty refresh leaves the previous stations in place: losing
// the directory must not stop a fleet that is already scanning.
func (r *scanRoster) Refresh(ctx context.Context) error {
	r.refreshMu.Lock()
	defer r.refreshMu.Unlock()
	if r.refresh == nil {
		return nil
	}
	stations, err := r.refresh(ctx)
	if err != nil {
		r.logf("scan: directory refresh failed, keeping %d station(s): %v", len(r.Snapshot()), err)
		return err
	}
	if len(stations) == 0 {
		r.logf("scan: directory refresh returned no stations, keeping the previous list")
		return nil
	}
	r.mu.Lock()
	old := append([]string(nil), r.dynamic...)
	r.mu.Unlock()
	r.SetDynamic(stations)
	r.mu.Lock()
	r.lastRefresh = time.Now()
	r.mu.Unlock()
	added, removed := countRosterDelta(old, stations)
	r.logf("scan: directory refresh: %d station(s), +%d new, -%d gone", len(stations), added, removed)
	return nil
}

// StartRefresh runs Refresh on a ticker until ctx ends. It is a no-op when
// the caller supplied no refresh function or a non-positive interval.
func (r *scanRoster) StartRefresh(ctx context.Context) {
	if r.refresh == nil || r.interval <= 0 {
		return
	}
	go func() {
		// Until the first successful refresh, retry quickly instead of
		// waiting a full interval: the initial fetch at process start runs
		// before the network is necessarily up (notably right after an app
		// launch on a phone), and an hour of empty roster reads as a dead
		// app. After a success, fall back to the configured cadence.
		const failureRetry = 2 * time.Minute
		next := failureRetry
		r.refreshMu.Lock()
		if !r.lastRefresh.IsZero() {
			next = r.interval
		}
		r.refreshMu.Unlock()
		timer := time.NewTimer(next)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				if err := r.Refresh(ctx); err != nil {
					next = failureRetry
				} else {
					next = r.interval
				}
				timer.Reset(next)
			}
		}
	}()
}

func countRosterDelta(old, next []string) (added, removed int) {
	oldKeys := map[string]bool{}
	for _, station := range old {
		parts := strings.SplitN(station, "|", 2)
		if len(parts) == 2 {
			oldKeys[stationHostPortKey(parts[1])] = true
		}
	}
	newKeys := map[string]bool{}
	for _, station := range next {
		parts := strings.SplitN(station, "|", 2)
		if len(parts) == 2 {
			newKeys[stationHostPortKey(parts[1])] = true
		}
	}
	for key := range newKeys {
		if !oldKeys[key] {
			added++
		}
	}
	for key := range oldKeys {
		if !newKeys[key] {
			removed++
		}
	}
	return added, removed
}

// OpenWebRX profile rotation. A visit scans one profile so it stays within
// the politeness budget; successive visits start at the next profile. The
// first visit uses the server default, because the profile names are only
// known after connecting.
var (
	openProfileMu      sync.Mutex
	openProfilesByHost = map[string][]string{}
	openProfileNext    = map[string]int{}
)

func nextOpenWebRXProfile(host string) string {
	openProfileMu.Lock()
	defer openProfileMu.Unlock()
	profiles := openProfilesByHost[host]
	if len(profiles) == 0 {
		return ""
	}
	index := openProfileNext[host] % len(profiles)
	if index < 0 {
		index = 0
	}
	openProfileNext[host] = (index + 1) % len(profiles)
	return profiles[index]
}

func noteOpenWebRXProfiles(host, used string, names []string) {
	clean := make([]string, 0, len(names))
	seen := map[string]bool{}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" || seen[strings.ToLower(name)] {
			continue
		}
		seen[strings.ToLower(name)] = true
		clean = append(clean, name)
	}
	if len(clean) == 0 {
		return
	}
	openProfileMu.Lock()
	if !reflect.DeepEqual(openProfilesByHost[host], clean) {
		openProfilesByHost[host] = clean
		openProfileNext[host] = 0
	}
	if used != "" {
		for i, name := range clean {
			if strings.EqualFold(name, used) {
				openProfileNext[host] = (i + 1) % len(clean)
				break
			}
		}
	}
	if openProfileNext[host] >= len(clean) {
		openProfileNext[host] = 0
	}
	// Unlock before persisting: persistRotation re-enters both cursor
	// mutexes, so the deferred unlock below would deadlock it.
	openProfileMu.Unlock()
	persistRotation()
}

func resetOpenWebRXRotation(host string) {
	openProfileMu.Lock()
	delete(openProfilesByHost, host)
	delete(openProfileNext, host)
	openProfileMu.Unlock()
	persistRotation()
}

// Kiwi HF target rotation. A session stops at MaxTargets or MaxSession, so
// the next visit must resume after the last target it actually processed;
// otherwise the first few entries are rescanned forever and later entries
// never get a slot.
var (
	kiwiCursorMu     sync.Mutex
	kiwiCursorByHost = map[string]string{}
)

func rotateHFTargets(targets []decode.HFTarget, last string) []decode.HFTarget {
	if len(targets) == 0 {
		return nil
	}
	start := 0
	for i, target := range targets {
		if target.Name == last {
			start = (i + 1) % len(targets)
			break
		}
	}
	out := make([]decode.HFTarget, 0, len(targets))
	out = append(out, targets[start:]...)
	out = append(out, targets[:start]...)
	return out
}

func nextKiwiTargets(host string, targets []decode.HFTarget) []decode.HFTarget {
	kiwiCursorMu.Lock()
	last := kiwiCursorByHost[host]
	kiwiCursorMu.Unlock()
	return rotateHFTargets(targets, last)
}

func noteKiwiProgress(host string, results []decode.HFResult) {
	if len(results) == 0 {
		return
	}
	last := strings.TrimSpace(results[len(results)-1].Target.Name)
	if last == "" {
		return
	}
	kiwiCursorMu.Lock()
	kiwiCursorByHost[host] = last
	kiwiCursorMu.Unlock()
	persistRotation()
}

func resetKiwiRotation(host string) {
	kiwiCursorMu.Lock()
	delete(kiwiCursorByHost, host)
	kiwiCursorMu.Unlock()
	persistRotation()
}

// emptyRosterAlerter makes an empty scan roster loud without flooding the
// log: the scan loop re-enters every 20 s, so the empty state is logged at
// most once per emptyRosterLogInterval, plus once when the roster recovers.
type emptyRosterAlerter struct {
	mu       sync.Mutex
	logf     func(string, ...interface{})
	now      func() time.Time
	lastLog  time.Time
	wasEmpty bool
}

const emptyRosterLogInterval = 10 * time.Minute

func newEmptyRosterAlerter(logf func(string, ...interface{})) *emptyRosterAlerter {
	if logf == nil {
		logf = func(string, ...interface{}) {}
	}
	return &emptyRosterAlerter{logf: logf, now: time.Now}
}

// empty reports one roster-empty pass. reason names where the roster ran
// dry; detail says what each stage saw.
func (a *emptyRosterAlerter) empty(reason, detail string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	if a.wasEmpty && now.Sub(a.lastLog) < emptyRosterLogInterval {
		return
	}
	a.wasEmpty = true
	a.lastLog = now
	a.logf("scan: ROSTER EMPTY (%s): %s; nothing will scan until this changes", reason, detail)
}

// populated reports a pass with stations; after an empty stretch it logs
// the recovery once.
func (a *emptyRosterAlerter) populated(n int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.wasEmpty {
		return
	}
	a.wasEmpty = false
	a.logf("scan: roster populated again: %d station(s) to scan", n)
}
