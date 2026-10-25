package fleet

import (
	"testing"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/probe"
)

var testNow = time.Date(2026, 10, 25, 13, 0, 0, 0, time.UTC)

// fixture builds a catalog snapshot with HF-capable stations of mixed
// quality plus a VHF-only and a dead one.
func fixture() probe.Snapshot {
	at := testNow.Add(-10 * time.Minute)
	c := probe.NewCatalog()
	upd := func(key string, failures int, checked time.Time, min, max int64, pw bool) {
		c.Update(probe.Record{Key: key, Kind: probe.KindKiwi, Reachable: true,
			Verdict: probe.VerdictOK, CheckedAt: checked, ConsecFailures: failures,
			Capability: probe.Capability{FreqMinHz: min, FreqMaxHz: max, Passworded: pw}})
	}
	upd("hf-a", 0, at, 0, 30_000_000, false)
	upd("hf-b", 0, at.Add(time.Minute), 0, 30_000_000, false)
	upd("hf-flaky", 3, at.Add(2*time.Minute), 0, 30_000_000, false)
	upd("hf-pw", 0, at.Add(3*time.Minute), 0, 30_000_000, true)
	c.Update(probe.Record{Key: "vhf", Kind: probe.KindKiwi, Reachable: true, Verdict: probe.VerdictOK,
		CheckedAt: at, Capability: probe.Capability{FreqMinHz: 144_000_000, FreqMaxHz: 146_000_000}})
	c.Update(probe.Record{Key: "hf-dead", Kind: probe.KindKiwi, Reachable: false, CheckedAt: at,
		Capability: probe.Capability{FreqMinHz: 0, FreqMaxHz: 30_000_000}})
	return c.Snapshot()
}

var ft8job = Job{ID: "ft8-20m", FreqHz: 14_074_000, Mode: "usb", Label: "FT8 20m", MaxAge: 2 * time.Hour}

func TestPlanAssignsBestRankedMatch(t *testing.T) {
	p := NewPlanner()
	plan := p.Plan(fixture(), []Job{ft8job}, testNow)
	if len(plan.Assigned) != 1 {
		t.Fatalf("assigned: %+v unplaced: %+v", plan.Assigned, plan.Unplaced)
	}
	// Select ranking: fewest failures, then freshest. hf-b (0f, freshest
	// 0-failure) beats hf-a; hf-flaky (3f) and hf-pw (passworded) are out.
	if got := plan.Assigned[0].Station.Key; got != "hf-b" {
		t.Fatalf("assigned to %s, want hf-b", got)
	}
	if len(plan.Unplaced) != 0 {
		t.Fatalf("unplaced: %+v", plan.Unplaced)
	}
}

func TestPlanRespectsPerStationCapacity(t *testing.T) {
	p := NewPlanner()
	p.MaxPerStation = 1
	jobs := []Job{ft8job, {ID: "ft8-40m", FreqHz: 7_074_000, Mode: "usb", MaxAge: 2 * time.Hour}}
	plan := p.Plan(fixture(), jobs, testNow)
	if len(plan.Assigned) != 2 {
		t.Fatalf("assigned: %+v", plan.Assigned)
	}
	if plan.Assigned[0].Station.Key == plan.Assigned[1].Station.Key {
		t.Fatalf("same station twice at capacity 1: %+v", plan.Assigned)
	}
}

func TestPlanReportsUnplacedJobs(t *testing.T) {
	p := NewPlanner()
	// 2m band: only "vhf" covers it but a MaxAge bound keeps it eligible;
	// 70cm: nobody covers it.
	jobs := []Job{
		{ID: "fm-2m", FreqHz: 145_500_000, Mode: "nfm", MaxAge: 2 * time.Hour},
		{ID: "fm-70cm", FreqHz: 433_500_000, Mode: "nfm", MaxAge: 2 * time.Hour},
	}
	plan := p.Plan(fixture(), jobs, testNow)
	if len(plan.Assigned) != 1 || plan.Assigned[0].Station.Key != "vhf" {
		t.Fatalf("assigned: %+v", plan.Assigned)
	}
	if len(plan.Unplaced) != 1 || plan.Unplaced[0].ID != "fm-70cm" {
		t.Fatalf("unplaced: %+v", plan.Unplaced)
	}
}

func TestPlanSticksToPreviousStation(t *testing.T) {
	p := NewPlanner()
	first := p.Plan(fixture(), []Job{ft8job}, testNow)
	second := p.Plan(fixture(), []Job{ft8job}, testNow.Add(10*time.Minute))
	if first.Assigned[0].Station.Key != second.Assigned[0].Station.Key {
		t.Fatalf("job migrated %s -> %s without cause; retuning is costly, assignments stick",
			first.Assigned[0].Station.Key, second.Assigned[0].Station.Key)
	}
}

func TestPlanFailureCooldownMovesJob(t *testing.T) {
	p := NewPlanner()
	first := p.Plan(fixture(), []Job{ft8job}, testNow)
	failed := first.Assigned[0].Station.Key
	p.RecordOutcome("ft8-20m", failed, false, testNow)
	second := p.Plan(fixture(), []Job{ft8job}, testNow.Add(time.Minute))
	if second.Assigned[0].Station.Key == failed {
		t.Fatalf("job stayed on %s right after a failed outcome", failed)
	}
	// After the cooldown the pair is eligible again.
	later := second.Assigned[0].Station.Key
	p.RecordOutcome("ft8-20m", later, false, testNow.Add(time.Minute))
	third := p.Plan(fixture(), []Job{ft8job}, testNow.Add(time.Hour))
	if len(third.Assigned) != 1 {
		t.Fatalf("third plan: %+v unplaced %+v", third.Assigned, third.Unplaced)
	}
}

func TestPlanNeverAssignsPasswordedOrDead(t *testing.T) {
	p := NewPlanner()
	plan := p.Plan(fixture(), []Job{ft8job}, testNow)
	for _, a := range plan.Assigned {
		if a.Station.Key == "hf-pw" || a.Station.Key == "hf-dead" {
			t.Fatalf("assigned %s (passworded or dead)", a.Station.Key)
		}
	}
}

func TestPlanRequiresMaxAge(t *testing.T) {
	p := NewPlanner()
	// Assignment without a staleness bound is the footgun QA flagged:
	// the planner refuses rather than accept week-old probes.
	plan := p.Plan(fixture(), []Job{{ID: "j", FreqHz: 14_074_000}}, testNow)
	if len(plan.Unplaced) != 1 {
		t.Fatalf("job without MaxAge must not be placed: %+v", plan.Assigned)
	}
}

func TestPlanStickyJobNotStolenByEarlierJob(t *testing.T) {
	p := NewPlanner()
	// First plan: stickyJob lands on the best station (hf-b).
	first := p.Plan(fixture(), []Job{ft8job}, testNow)
	if first.Assigned[0].Station.Key != "hf-b" {
		t.Fatalf("setup: sticky job on %s, want hf-b", first.Assigned[0].Station.Key)
	}
	// Second plan: a NEW job earlier in the list also matches hf-b best.
	// Without sticky-first placement the new job steals hf-b.
	other := Job{ID: "wspr-20m", FreqHz: 14_095_600, Mode: "usb", Label: "WSPR 20m", MaxAge: 2 * time.Hour}
	plan := p.Plan(fixture(), []Job{other, ft8job}, testNow.Add(time.Minute))
	got := map[string]string{}
	for _, a := range plan.Assigned {
		got[a.Job.ID] = a.Station.Key
	}
	if got["ft8-20m"] != "hf-b" {
		t.Fatalf("sticky job lost its station: %v", got)
	}
	if got["wspr-20m"] != "hf-a" {
		t.Fatalf("new job should take the next-best: %v", got)
	}
}

func TestPlannerConcurrentUse(t *testing.T) {
	p := NewPlanner()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 200; i++ {
			p.RecordOutcome("ft8-20m", "hf-a", i%2 == 0, testNow)
		}
		close(done)
	}()
	for i := 0; i < 200; i++ {
		p.Plan(fixture(), []Job{ft8job}, testNow)
	}
	<-done
}

func TestCooldownEntriesPrunedAfterExpiry(t *testing.T) {
	p := NewPlanner()
	p.RecordOutcome("ft8-20m", "hf-b", false, testNow)
	if p.cooldownSize() != 1 {
		t.Fatalf("setup: cooldown size %d", p.cooldownSize())
	}
	// Past the 30min cooldown: the next Plan prunes the expired entry.
	p.Plan(fixture(), []Job{ft8job}, testNow.Add(31*time.Minute))
	if p.cooldownSize() != 0 {
		t.Fatalf("expired cooldown entries not pruned: %d", p.cooldownSize())
	}
}

func TestPlanPrunesStaleStickinessMemory(t *testing.T) {
	// Windowed campaigns mint a fresh job ID per window; the planner's
	// stickiness map must drop IDs that left the campaign (QA review).
	p := NewPlanner()
	p.Plan(fixture(), []Job{ft8job}, testNow)
	window2 := Job{ID: "ft8-20m@14:00", FreqHz: 14_074_000, Mode: "usb", Label: "FT8 20m", MaxAge: 2 * time.Hour}
	p.Plan(fixture(), []Job{window2}, testNow.Add(time.Hour))
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, kept := p.last["ft8-20m"]; kept {
		t.Fatalf("stale job ID kept in stickiness memory: %v", p.last)
	}
	if _, kept := p.last[window2.ID]; !kept {
		t.Fatalf("current job ID pruned by mistake: %v", p.last)
	}
}
