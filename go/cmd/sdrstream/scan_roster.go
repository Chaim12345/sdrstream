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

func (r *scanRoster) AddManual(stations ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, station := range stations {
		station = strings.TrimSpace(station)
		if station != "" {
			r.manual = append(r.manual, station)
		}
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
		key := stationHostPortKey(parts[0], parts[1])
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
		timer := time.NewTimer(r.interval)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				_ = r.Refresh(ctx)
				timer.Reset(r.interval)
			}
		}
	}()
}

func countRosterDelta(old, next []string) (added, removed int) {
	oldKeys := map[string]bool{}
	for _, station := range old {
		parts := strings.SplitN(station, "|", 2)
		if len(parts) == 2 {
			oldKeys[stationHostPortKey(parts[0], parts[1])] = true
		}
	}
	newKeys := map[string]bool{}
	for _, station := range next {
		parts := strings.SplitN(station, "|", 2)
		if len(parts) == 2 {
			newKeys[stationHostPortKey(parts[0], parts[1])] = true
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
