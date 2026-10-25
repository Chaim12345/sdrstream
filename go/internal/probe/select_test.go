package probe

import (
	"testing"
	"time"
)

func selectFixture() Snapshot {
	at := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	c := NewCatalog()
	c.Update(Record{Key: "hf-kiwi", Kind: KindKiwi, Reachable: true, CheckedAt: at,
		Capability: Capability{FreqMinHz: 0, FreqMaxHz: 30_000_000, UsersMax: 4}})
	c.Update(Record{Key: "hf-owrx", Kind: KindOpenWebRX, Reachable: true, CheckedAt: at.Add(time.Minute),
		Capability: Capability{FreqMinHz: 100_000, FreqMaxHz: 30_000_000, Profiles: []string{"20m"}}})
	c.Update(Record{Key: "vhf-owrx", Kind: KindOpenWebRX, Reachable: true, CheckedAt: at.Add(2 * time.Minute),
		Capability: Capability{FreqMinHz: 144_000_000, FreqMaxHz: 146_000_000}})
	c.Update(Record{Key: "hf-websdr", Kind: KindWebSDR, Reachable: true, CheckedAt: at.Add(3 * time.Minute)})
	// ^ WebSDR: no machine-readable coverage at all.
	c.Update(Record{Key: "hf-pw", Kind: KindKiwi, Reachable: true, CheckedAt: at.Add(4 * time.Minute),
		Capability: Capability{FreqMinHz: 0, FreqMaxHz: 30_000_000, Passworded: true}})
	c.Update(Record{Key: "hf-dead", Kind: KindKiwi, Reachable: false, CheckedAt: at.Add(5 * time.Minute),
		Capability: Capability{FreqMinHz: 0, FreqMaxHz: 30_000_000}})
	c.Update(Record{Key: "hf-stale", Kind: KindKiwi, Reachable: true, CheckedAt: at.Add(-2 * time.Hour),
		ConsecFailures: 1, Capability: Capability{FreqMinHz: 0, FreqMaxHz: 30_000_000}})
	return c.Snapshot()
}

var selectNow = time.Date(2026, 10, 25, 13, 0, 0, 0, time.UTC)

func keysOf(recs []*Record) []string {
	var out []string
	for _, r := range recs {
		out = append(out, r.Key)
	}
	return out
}

func TestSelectFiltersByFrequencyCoverage(t *testing.T) {
	got := Select(selectFixture(), Requirement{FreqHz: 14_074_000}, selectNow, 0)
	// hf-websdr has no coverage info: excluded, never claimed.
	// hf-dead is unreachable: excluded. vhf-owrx doesn't cover 14 MHz.
	// Ranking: fewest consecutive failures first, then freshest check.
	want := []string{"hf-pw", "hf-owrx", "hf-kiwi", "hf-stale"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", keysOf(got), want)
	}
	for i := range want {
		if got[i].Key != want[i] {
			t.Fatalf("order got %v want %v", keysOf(got), want)
		}
	}
}

func TestSelectRankingFreshBeforeStale(t *testing.T) {
	// Freshest confirmed-alive first, fewest failures first: hf-stale has
	// ConsecFailures=1 and a 2h-old check, so it ranks last among matches.
	got := Select(selectFixture(), Requirement{FreqHz: 7_000_000}, selectNow, 0)
	if got[len(got)-1].Key != "hf-stale" {
		t.Fatalf("stale/flaky station should rank last: %v", keysOf(got))
	}
}

func TestSelectKindAndPasswordFilters(t *testing.T) {
	got := Select(selectFixture(), Requirement{FreqHz: 14_074_000, Kinds: []string{KindOpenWebRX}}, selectNow, 0)
	if len(got) != 1 || got[0].Key != "hf-owrx" {
		t.Fatalf("kind filter: %v", keysOf(got))
	}
	got = Select(selectFixture(), Requirement{FreqHz: 14_074_000, NoPassword: true}, selectNow, 0)
	for _, r := range got {
		if r.Capability.Passworded {
			t.Fatalf("NoPassword returned %s", r.Key)
		}
	}
}

func TestSelectNoFrequencyIncludesCoveragelessButStaysHonest(t *testing.T) {
	// Without a frequency requirement, coverage-unknown stations (WebSDR)
	// are eligible: the caller isn't asking what they can hear.
	got := Select(selectFixture(), Requirement{Kinds: []string{KindWebSDR}}, selectNow, 0)
	if len(got) != 1 || got[0].Key != "hf-websdr" {
		t.Fatalf("got %v", keysOf(got))
	}
}

func TestSelectLimit(t *testing.T) {
	got := Select(selectFixture(), Requirement{FreqHz: 14_074_000}, selectNow, 2)
	if len(got) != 2 || got[0].Key != "hf-pw" || got[1].Key != "hf-owrx" {
		t.Fatalf("limit: %v", keysOf(got))
	}
}

func TestSelectSkipsBusyByDefault(t *testing.T) {
	at := time.Date(2026, 10, 25, 12, 55, 0, 0, time.UTC)
	c := NewCatalog()
	c.Update(Record{Key: "busy-kiwi", Kind: KindKiwi, Reachable: true, Verdict: VerdictBusy, CheckedAt: at,
		Capability: Capability{FreqMinHz: 0, FreqMaxHz: 30_000_000, UsersMax: 4}})
	c.Update(Record{Key: "free-kiwi", Kind: KindKiwi, Reachable: true, Verdict: VerdictOK, CheckedAt: at,
		Capability: Capability{FreqMinHz: 0, FreqMaxHz: 30_000_000, UsersMax: 4}})
	snap := c.Snapshot()

	// A busy receiver answered but has no free slot: it is not a candidate
	// for immediate work unless the caller explicitly allows it.
	got := Select(snap, Requirement{FreqHz: 14_074_000}, selectNow, 0)
	if len(got) != 1 || got[0].Key != "free-kiwi" {
		t.Fatalf("busy must be skipped by default: %v", keysOf(got))
	}
	got = Select(snap, Requirement{FreqHz: 14_074_000, IncludeBusy: true}, selectNow, 0)
	if len(got) != 2 {
		t.Fatalf("IncludeBusy: %v", keysOf(got))
	}
}

func TestSelectMaxAge(t *testing.T) {
	at := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	c := NewCatalog()
	c.Update(Record{Key: "fresh", Kind: KindKiwi, Reachable: true, CheckedAt: at,
		Capability: Capability{FreqMinHz: 0, FreqMaxHz: 30_000_000}})
	c.Update(Record{Key: "old", Kind: KindKiwi, Reachable: true, CheckedAt: at.Add(-48 * time.Hour),
		Capability: Capability{FreqMinHz: 0, FreqMaxHz: 30_000_000}})
	c.Update(Record{Key: "edge", Kind: KindKiwi, Reachable: true, CheckedAt: at.Add(-2 * time.Hour),
		Capability: Capability{FreqMinHz: 0, FreqMaxHz: 30_000_000}})
	snap := c.Snapshot()
	now := at // "now" is 2h after fresh, 50h after old

	got := Select(snap, Requirement{FreqHz: 14_074_000, MaxAge: 2 * time.Hour}, now, 0)
	// old is 48h stale: excluded. edge is exactly MaxAge old: included
	// (the bound is inclusive).
	if len(got) != 2 || got[0].Key != "fresh" || got[1].Key != "edge" {
		t.Fatalf("MaxAge: %v", keysOf(got))
	}
	// Zero MaxAge = no bound (documented default).
	got = Select(snap, Requirement{FreqHz: 14_074_000}, now, 0)
	if len(got) != 3 {
		t.Fatalf("no bound: %v", keysOf(got))
	}
}
