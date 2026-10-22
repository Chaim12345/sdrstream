package feeds

import (
	"sort"
	"time"
)

// Metrics counts first sightings of items per source so the operator can
// see real throughput (items per hour) instead of guessing. A first
// sighting is an item ID not seen from that source in the last hour;
// refreshes of a known ID (a polled feed re-reporting the same aircraft)
// do not count.
const (
	metricsWindow  = time.Hour
	metricsBuckets = 60 // one-minute buckets
)

type srcMetrics struct {
	total    int64
	buckets  [metricsBuckets]int64
	bucketAt [metricsBuckets]int64 // unix minute each bucket currently holds
	lastItem time.Time
}

func (sm *srcMetrics) add(now time.Time) {
	min := now.Unix() / 60
	i := int(min % metricsBuckets)
	if sm.bucketAt[i] != min {
		sm.bucketAt[i] = min
		sm.buckets[i] = 0
	}
	sm.buckets[i]++
	sm.total++
	sm.lastItem = now
}

func (sm *srcMetrics) lastHour(now time.Time) int64 {
	min := now.Unix() / 60
	var n int64
	for i := 0; i < metricsBuckets; i++ {
		if age := min - sm.bucketAt[i]; age >= 0 && age < metricsBuckets {
			n += sm.buckets[i]
		}
	}
	return n
}

// noteSeenLocked records a sighting and reports whether it was the first
// in the metrics window. m.mu must be held.
func (m *Manager) noteSeenLocked(it Item, now time.Time) bool {
	if m.seen == nil {
		m.seen = map[string]time.Time{}
	}
	key := it.Source + "\x00" + it.ID
	if t, ok := m.seen[key]; ok && now.Sub(t) < metricsWindow {
		return false
	}
	m.seen[key] = now
	if m.metrics == nil {
		m.metrics = map[string]*srcMetrics{}
	}
	sm := m.metrics[it.Source]
	if sm == nil {
		sm = &srcMetrics{}
		m.metrics[it.Source] = sm
	}
	sm.add(now)
	// Bound the seen set: drop expired keys once it grows past 4x capacity.
	if len(m.seen) > 4*m.cap {
		for k, t := range m.seen {
			if now.Sub(t) >= metricsWindow {
				delete(m.seen, k)
			}
		}
	}
	return true
}

// SourceMetrics is one source's throughput and health.
type SourceMetrics struct {
	Name         string    `json:"name"`
	Kind         string    `json:"kind"`
	State        string    `json:"state"`
	ItemsNow     int       `json:"items_now"`
	NewLastHour  int64     `json:"new_last_hour"`
	NewSinceBoot int64     `json:"new_since_boot"`
	LastItem     time.Time `json:"last_item,omitempty"`
	LastOK       time.Time `json:"last_ok,omitempty"`
	LastError    string    `json:"last_error,omitempty"`
}

// MetricsReport is the payload for /api/metrics.json (feeds part).
type MetricsReport struct {
	GeneratedAt time.Time       `json:"generated_at"`
	Sources     []SourceMetrics `json:"sources"`
	SourcesLive int             `json:"sources_live"`
	NewLastHour int64           `json:"new_last_hour"`
	ItemsNow    int             `json:"items_now"`
}

// Metrics reports per-source throughput. Counts are of items this process
// observed, so they reset on restart (new_since_boot makes that explicit).
func (m *Manager) Metrics() MetricsReport {
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	rep := MetricsReport{GeneratedAt: now, Sources: []SourceMetrics{}}
	for name, st := range m.sources {
		sm := SourceMetrics{Name: name, Kind: st.status.Kind, State: st.status.State,
			ItemsNow: countFor(m.items, name), LastOK: st.status.LastOK, LastError: st.status.LastError}
		if x := m.metrics[name]; x != nil {
			sm.NewLastHour = x.lastHour(now)
			sm.NewSinceBoot = x.total
			sm.LastItem = x.lastItem
		}
		if sm.State == "live" {
			rep.SourcesLive++
		}
		rep.NewLastHour += sm.NewLastHour
		rep.ItemsNow += sm.ItemsNow
		rep.Sources = append(rep.Sources, sm)
	}
	sort.Slice(rep.Sources, func(i, j int) bool { return rep.Sources[i].Name < rep.Sources[j].Name })
	return rep
}
