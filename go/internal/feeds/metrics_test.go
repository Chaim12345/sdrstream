package feeds

import (
	"testing"
	"time"
)

func TestMetricsCountsFirstSightingsOnly(t *testing.T) {
	m := New(100, func(string, ...any) {})
	m.Add(Item{ID: "a", Source: "s1", Label: "A"}, Item{ID: "b", Source: "s1", Label: "B"})
	m.Add(Item{ID: "a", Source: "s1", Label: "A"}) // refresh, not new
	m.Add(Item{ID: "a", Source: "s2", Label: "A"}) // same ID, other source: new
	rep := m.Metrics()
	got := map[string]int64{}
	for _, s := range rep.Sources {
		got[s.Name] = s.NewLastHour
	}
	if got["s1"] != 2 || got["s2"] != 1 || rep.NewLastHour != 3 {
		t.Fatalf("counts = %v total %d", got, rep.NewLastHour)
	}
}

func TestMetricsBucketsExpire(t *testing.T) {
	var sm srcMetrics
	now := time.Unix(1_700_000_000, 0)
	sm.add(now.Add(-90 * time.Minute))
	sm.add(now.Add(-30 * time.Minute))
	sm.add(now)
	if n := sm.lastHour(now); n != 2 {
		t.Fatalf("lastHour = %d, want 2", n)
	}
	if sm.total != 3 {
		t.Fatalf("total = %d", sm.total)
	}
}

func TestMetricsPolledReplaceStillCountsOnce(t *testing.T) {
	m := New(100, func(string, ...any) {})
	// Poller replaces its cache each cycle: delete then re-add same IDs.
	m.Add(Item{ID: "x", Source: "p", Label: "X"})
	m.mu.Lock()
	delete(m.items, "x")
	m.mu.Unlock()
	m.Add(Item{ID: "x", Source: "p", Label: "X"})
	if n := m.Metrics().NewLastHour; n != 1 {
		t.Fatalf("new = %d, want 1", n)
	}
}
