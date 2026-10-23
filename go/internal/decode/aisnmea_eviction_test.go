package decode

import (
	"fmt"
	"testing"
	"time"
)

// R5: the two-tier eviction (stale first, then oldest) was only covered
// for the aggregate cap. These tests pin the tiers, key reuse after
// eviction, and completion-time cleanup.

// seedAssembly installs one in-flight assembly with a chosen age.
func seedAssembly(a *AISAssembler, key string, seen time.Time) {
	a.parts[key] = map[int]string{1: "payload"}
	a.total[key] = 2
	a.rawLog[key] = []string{"raw"}
	a.seen[key] = seen
	a.order = append(a.order, key)
}

func TestAISEvictionPrefersStaleOverOldest(t *testing.T) {
	a := NewAISAssembler()
	now := time.Now()
	// Fill the cap with fresh assemblies, plus one stale straggler whose
	// seen time is older than aisStaleAfter - the cap is exceeded, so the
	// stale tier must go first even though a fresh key is older by order.
	staleKey := "A|stale"
	seedAssembly(a, staleKey, now.Add(-aisStaleAfter-time.Minute))
	for i := 0; i < aisMaxInFlight; i++ {
		seedAssembly(a, fmt.Sprintf("A|fresh-%d", i), now.Add(-time.Duration(i)*time.Second))
	}
	a.mu.Lock()
	a.capLocked(now)
	_, staleKept := a.parts[staleKey]
	tracked := len(a.parts)
	a.mu.Unlock()
	if staleKept {
		t.Fatal("stale assembly survived eviction while fresh ones were dropped")
	}
	if tracked > aisMaxInFlight {
		t.Fatalf("still tracking %d, cap %d", tracked, aisMaxInFlight)
	}
}

func TestAISEvictionDropsOldestWhenNothingStale(t *testing.T) {
	a := NewAISAssembler()
	now := time.Now()
	oldest := "A|oldest"
	seedAssembly(a, oldest, now.Add(-time.Minute))
	for i := 0; i < aisMaxInFlight; i++ {
		seedAssembly(a, fmt.Sprintf("A|k%d", i), now)
	}
	a.mu.Lock()
	a.capLocked(now)
	_, oldestKept := a.parts[oldest]
	tracked := len(a.parts)
	orderLen := len(a.order)
	a.mu.Unlock()
	if oldestKept {
		t.Fatal("oldest assembly kept while newer ones were dropped")
	}
	// capLocked runs when a NEW key is about to arrive, so it evicts down
	// to cap-1, leaving headroom for the caller's insert.
	if tracked != aisMaxInFlight-1 {
		t.Fatalf("tracked = %d, want cap-1 headroom %d after eviction", tracked, aisMaxInFlight-1)
	}
	if orderLen != tracked {
		t.Fatalf("order has %d keys, parts has %d - eviction desynced the queue", orderLen, tracked)
	}
}

func TestAISKeyReuseAfterEvictionStartsClean(t *testing.T) {
	a := NewAISAssembler()
	now := time.Now()
	key := "A|7"
	// An abandoned fragment 1 with a stale clock, then evicted.
	seedAssembly(a, key, now.Add(-aisStaleAfter-time.Minute))
	a.mu.Lock()
	a.evictStaleLocked(now)
	a.mu.Unlock()
	// Feed a fresh two-part message on the same key: it must assemble
	// from scratch, not mix the abandoned fragment in.
	first := nmeaWithChecksum("!AIVDM,2,1,7,A,55NSAu42CqpGD8<CL01@u8n04h,0")
	if _, _, err := a.Feed(first); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	fragCount := len(a.parts[key])
	orderCount := 0
	for _, k := range a.order {
		if k == key {
			orderCount++
		}
	}
	a.mu.Unlock()
	if fragCount != 1 {
		t.Fatalf("reused key holds %d fragments, want exactly the fresh one", fragCount)
	}
	if orderCount != 1 {
		t.Fatalf("order lists the reused key %d times, want 1", orderCount)
	}
}

func TestAISCompletionRemovesTracking(t *testing.T) {
	a := NewAISAssembler()
	one := nmeaWithChecksum("!AIVDM,2,1,3,A,55NSAu42CqpGD8<CL01@u8n04h,0")
	two := nmeaWithChecksum("!AIVDM,2,2,3,A,00000000000,2")
	if _, _, err := a.Feed(one); err != nil {
		t.Fatal(err)
	}
	br, raw, err := a.Feed(two)
	if err != nil {
		t.Fatal(err)
	}
	if br == nil || raw == "" {
		t.Fatal("complete two-part message returned no reader")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for name, m := range map[string]int{"parts": len(a.parts), "fill": len(a.fill), "total": len(a.total), "rawLog": len(a.rawLog), "seen": len(a.seen), "order": len(a.order)} {
		if m != 0 {
			t.Errorf("%s still tracks %d entries after completion", name, m)
		}
	}
}
