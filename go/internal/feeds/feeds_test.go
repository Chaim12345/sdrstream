package feeds

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestManagerAddDedupSnapshot(t *testing.T) {
	m := New(10, func(string, ...any) {})
	m.mu.Lock()
	m.source("adsb.lol", "aircraft", time.Minute)
	m.mu.Unlock()
	first := Item{ID: "adsb|abc", Source: "adsb.lol", Kind: "aircraft", Label: "RJA134", Time: time.Now().Add(-time.Minute)}
	m.Add(first)
	m.Add(Item{ID: "adsb|abc", Source: "adsb.lol", Kind: "aircraft", Label: "RJA134", Detail: "27000 ft", Time: time.Now()})
	m.Add(Item{ID: "aprs|4x1", Source: "aprs-is", Kind: "aprs", Label: "4X1RF", Time: time.Now()})
	snap := m.Snapshot(0)
	if len(snap.Items) != 2 {
		t.Fatalf("want 2 items, got %d", len(snap.Items))
	}
	var refreshed *Item
	for i := range snap.Items {
		if snap.Items[i].ID == "adsb|abc" {
			refreshed = &snap.Items[i]
		}
	}
	if refreshed == nil || refreshed.Detail != "27000 ft" {
		t.Fatalf("dedup did not refresh item: %+v", snap.Items)
	}
	if len(snap.Sources) != 2 || snap.Sources[0].Name != "adsb.lol" {
		t.Fatalf("sources not registered: %+v", snap.Sources)
	}
}

func TestManagerEvictsOldest(t *testing.T) {
	m := New(4, func(string, ...any) {})
	old := time.Now().Add(-time.Hour)
	for i := 0; i < 6; i++ {
		m.Add(Item{ID: string(rune('a' + i)), Source: "x", Kind: "k", Label: "L", Time: old.Add(time.Duration(i) * time.Minute)})
	}
	if got := len(m.Snapshot(0).Items); got > 5 { // cap 4 + quarter slack
		t.Fatalf("eviction did not run, %d items", got)
	}
	for _, it := range m.Snapshot(0).Items {
		if it.ID == "a" {
			t.Fatal("oldest item should have been evicted")
		}
	}
}

func TestManagerPruneBySourceAge(t *testing.T) {
	m := New(0, func(string, ...any) {})
	m.mu.Lock()
	m.source("adsb.lol", "aircraft", time.Minute)
	m.source("aprs-is", "aprs", time.Hour)
	m.mu.Unlock()
	stale := time.Now().Add(-2 * time.Minute)
	m.Add(Item{ID: "a", Source: "adsb.lol", Kind: "aircraft", Label: "OLD", Time: stale})
	m.Add(Item{ID: "b", Source: "aprs-is", Kind: "aprs", Label: "KEEP", Time: stale})
	m.prune(time.Now())
	snap := m.Snapshot(0)
	if len(snap.Items) != 1 || snap.Items[0].Label != "KEEP" {
		t.Fatalf("prune kept the wrong items: %+v", snap.Items)
	}
}

func TestFindByLabel(t *testing.T) {
	m := New(0, func(string, ...any) {})
	m.Add(Item{ID: "1", Source: "rbn", Kind: "beacon", Label: "4X1RF", Time: time.Now()})
	m.Add(Item{ID: "2", Source: "rbn", Kind: "beacon", Label: "4X1RF", Time: time.Now().Add(-2 * time.Hour)})
	got := m.FindByLabel("4x1rf", time.Hour)
	if len(got) != 1 || got[0].ID != "1" {
		t.Fatalf("FindByLabel window/source mismatch: %+v", got)
	}
}

type flakyStream struct{ name string }

func (f flakyStream) Name() string { return f.name }
func (f flakyStream) Kind() string { return "k" }
func (f flakyStream) Run(ctx context.Context, emit func(Item)) error {
	emit(Item{ID: "x", Label: "L"})
	return errors.New("boom")
}

func TestRunStreamReconnectsAndReports(t *testing.T) {
	m := New(0, func(string, ...any) {})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.RunStream(ctx, flakyStream{"flaky"}, 0); close(done) }()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snap := m.Snapshot(0)
		if len(snap.Sources) == 1 && snap.Sources[0].Items == 1 {
			s := snap.Sources[0]
			if s.State == "live" || s.State == "error" || s.State == "connecting" {
				cancel()
				<-done
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done
	t.Fatal("stream never reported items through the manager")
}
