package passplan

import (
	"testing"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/tracking"
)

var t0 = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

type fake struct {
	passes map[int][]Pass
}

func (f fake) Passes(m Monitored, _ time.Time, _ float64) ([]Pass, error) {
	return f.passes[m.NoradID], nil
}
func (f fake) Crossing(m Monitored, p Pass, el float64) (time.Time, time.Time, bool) {
	if p.PeakElevation < el {
		return time.Time{}, time.Time{}, false
	}
	return p.AOS.Add(time.Minute), p.LOS.Add(-time.Minute), true
}

func pass(startMin, durMin int, el float64) Pass {
	a := t0.Add(time.Duration(startMin) * time.Minute)
	l := a.Add(time.Duration(durMin) * time.Minute)
	return Pass{AOS: a, LOS: l, Peak: a.Add(l.Sub(a) / 2), PeakElevation: el}
}

func newP(f fake, strat string) *Planner {
	p := New(f, strat)
	p.Now = func() time.Time { return t0 }
	return p
}

func mon(id string, norad int, res string, prio int) Monitored {
	return Monitored{ID: id, Name: id, NoradID: norad, Resource: res, Priority: prio, Enabled: true, TaskStartElevation: 10}
}

func TestMinElevationFilterAndTaskWindow(t *testing.T) {
	p := newP(fake{map[int][]Pass{1: {pass(10, 10, 15), pass(100, 10, 45)}}}, "")
	st, err := p.Generate([]Monitored{mon("a", 1, "", 0)})
	if err != nil || st.Generated != 1 {
		t.Fatalf("stats=%+v err=%v", st, err)
	}
	o := p.Timeline()[0]
	if o.PeakEl != 45 || !o.TaskStart.Equal(o.AOS.Add(time.Minute)) {
		t.Fatalf("unexpected %+v", o)
	}
}

func TestDisabledSkipped(t *testing.T) {
	m := mon("a", 1, "", 0)
	m.Enabled = false
	p := newP(fake{map[int][]Pass{1: {pass(10, 10, 45)}}}, "")
	st, _ := p.Generate([]Monitored{m})
	if st.Generated != 0 {
		t.Fatal("disabled satellite planned")
	}
}

func TestRegenerateIdempotentAndUpdates(t *testing.T) {
	f := fake{map[int][]Pass{1: {pass(60, 10, 45)}}}
	p := newP(f, "")
	m := []Monitored{mon("a", 1, "", 0)}
	p.Generate(m)
	f.passes[1] = []Pass{pass(61, 10, 50)} // refreshed elements shift the pass
	st, _ := p.Generate(m)
	if st.Generated != 0 || st.Updated != 1 || len(p.Timeline()) != 1 || p.Timeline()[0].PeakEl != 50 {
		t.Fatalf("stats=%+v tl=%+v", st, p.Timeline())
	}
}

func TestRunningNotUpdated(t *testing.T) {
	f := fake{map[int][]Pass{1: {pass(60, 10, 45)}}}
	p := newP(f, "")
	m := []Monitored{mon("a", 1, "", 0)}
	p.Generate(m)
	id := p.Timeline()[0].ID
	if err := p.Start(id); err != nil {
		t.Fatal(err)
	}
	f.passes[1] = []Pass{pass(61, 10, 50)}
	st, _ := p.Generate(m)
	if st.Skipped != 1 || p.Timeline()[0].PeakEl != 45 {
		t.Fatalf("running obs was modified: %+v %+v", st, p.Timeline())
	}
}

func TestPriorityStrategyHigherElevationReplaces(t *testing.T) {
	f := fake{map[int][]Pass{1: {pass(60, 10, 30)}, 2: {pass(62, 10, 70)}}}
	p := newP(f, StrategyPriority)
	st, _ := p.Generate([]Monitored{mon("a", 1, "rx1", 0), mon("b", 2, "rx1", 1)})
	if st.Conflicts != 1 || st.Replaced != 1 {
		t.Fatalf("%+v", st)
	}
	for _, o := range p.Timeline() {
		if o.Satellite == "a" && o.Status != StatusCancelled {
			t.Fatal("loser not cancelled")
		}
		if o.Satellite == "b" && o.Status != StatusScheduled {
			t.Fatal("winner not scheduled")
		}
	}
}

func TestEqualElevationKeepsIncumbentAndDifferentResourceNoConflict(t *testing.T) {
	f := fake{map[int][]Pass{1: {pass(60, 10, 40)}, 2: {pass(62, 10, 40)}, 3: {pass(62, 10, 40)}}}
	p := newP(f, StrategyPriority)
	st, _ := p.Generate([]Monitored{mon("a", 1, "rx1", 0), mon("b", 2, "rx1", 1), mon("c", 3, "rx2", 2)})
	if st.Conflicts != 1 || st.Generated != 2 || st.Skipped != 1 {
		t.Fatalf("%+v", st)
	}
}

func TestSkipAndForce(t *testing.T) {
	f := fake{map[int][]Pass{1: {pass(60, 10, 30)}, 2: {pass(62, 10, 70)}}}
	ms := []Monitored{mon("a", 1, "rx1", 0), mon("b", 2, "rx1", 1)}
	p := newP(f, StrategySkip)
	st, _ := p.Generate(ms)
	if st.Generated != 1 || st.Skipped != 1 || p.Timeline()[0].Satellite != "a" {
		t.Fatalf("skip %+v", st)
	}
	p = newP(f, StrategyForce)
	st, _ = p.Generate(ms)
	if st.Generated != 2 || st.Conflicts != 0 {
		t.Fatalf("force %+v", st)
	}
}

func TestToleranceBoundary(t *testing.T) {
	// task windows are AOS+1..LOS-1; second pass starts 1 min after first LOS,
	// so the gap between task windows is 3 min: inside the 5 min tolerance.
	f := fake{map[int][]Pass{1: {pass(60, 10, 30)}, 2: {pass(71, 10, 30)}}}
	p := newP(f, StrategySkip)
	st, _ := p.Generate([]Monitored{mon("a", 1, "rx", 0), mon("b", 2, "rx", 1)})
	if st.Conflicts != 1 {
		t.Fatalf("expected tolerance conflict: %+v", st)
	}
	f = fake{map[int][]Pass{1: {pass(60, 10, 30)}, 2: {pass(80, 10, 30)}}}
	p = newP(f, StrategySkip)
	st, _ = p.Generate([]Monitored{mon("a", 1, "rx", 0), mon("b", 2, "rx", 1)})
	if st.Conflicts != 0 || st.Generated != 2 {
		t.Fatalf("unexpected conflict: %+v", st)
	}
}

func TestLifecycleAndMissed(t *testing.T) {
	f := fake{map[int][]Pass{1: {pass(60, 10, 45)}}}
	p := newP(f, "")
	p.Generate([]Monitored{mon("a", 1, "", 0)})
	id := p.Timeline()[0].ID
	if p.Next() == nil {
		t.Fatal("no next")
	}
	if err := p.Finish(id, true, ""); err == nil {
		t.Fatal("finish before start must fail")
	}
	p.Start(id)
	p.Finish(id, true, "ok")
	if p.Timeline()[0].Status != StatusCompleted {
		t.Fatal("not completed")
	}
	// Cancel + missed
	f.passes[1] = []Pass{pass(300, 10, 45)}
	p.Generate([]Monitored{mon("a", 1, "", 0)})
	p.Now = func() time.Time { return t0.Add(10 * time.Hour) }
	p.Generate([]Monitored{mon("a", 1, "", 0)})
	missed := 0
	for _, o := range p.Timeline() {
		if o.Status == StatusMissed {
			missed++
		}
	}
	if missed != 1 {
		t.Fatalf("missed=%d %+v", missed, p.Timeline())
	}
	if p.Cancel("nope") == nil {
		t.Fatal("cancel unknown must error")
	}
}

// Real SGP4: ISS elements, observer Jerusalem. Crossing window must sit
// inside the pass and be shorter than it; elevation at its edges ~ target.
const issTLE = `ISS (ZARYA)
1 25544U 98067A   26299.50000000  .00016717  00000-0  30000-3 0  9991
2 25544  51.6400 120.0000 0004000  60.0000 300.0000 15.50000000400009`

func TestTrackerFinderRealGeometry(t *testing.T) {
	tr, err := tracking.FromTLEText(issTLE, "ISS (ZARYA)")
	if err != nil {
		t.Fatal(err)
	}
	f := TrackerFinder{Trackers: map[int]*tracking.Tracker{25544: tr}, Lat: 31.77, Lon: 35.21}
	m := Monitored{ID: "iss", Name: "ISS", NoradID: 25544, Enabled: true, MinElevation: 10, TaskStartElevation: 5, LookaheadHours: 48}.defaults()
	start := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	ps, err := f.Passes(m, start, 48)
	if err != nil || len(ps) == 0 {
		t.Fatalf("passes=%d err=%v", len(ps), err)
	}
	checked := 0
	for _, p := range ps {
		if p.PeakElevation < 10 {
			continue
		}
		s, e, ok := f.Crossing(m, p, 5)
		if !ok || s.Before(p.AOS) || e.After(p.LOS) || !s.Before(e) {
			t.Fatalf("bad crossing %v %v ok=%v for %+v", s, e, ok, p)
		}
		for _, edge := range []time.Time{s, e} {
			la, _ := tr.LookAngle(edge, f.Lat, f.Lon, 0)
			if la.Elevation < 4.5 || la.Elevation > 5.5 {
				t.Fatalf("edge elevation %.2f not near 5", la.Elevation)
			}
		}
		checked++
	}
	if checked == 0 {
		t.Skip("no pass above 10 deg in window")
	}
	pl := New(f, "")
	pl.Now = func() time.Time { return start }
	st, err := pl.Generate([]Monitored{m})
	if err != nil || st.Generated != checked {
		t.Fatalf("generated=%d want %d err=%v", st.Generated, checked, err)
	}
	st2, _ := pl.Generate([]Monitored{m})
	if st2.Generated != 0 {
		t.Fatalf("regenerate duplicated: %+v", st2)
	}
	if _, err := f.Passes(Monitored{NoradID: 1}, start, 1); err == nil {
		t.Fatal("expected missing tracker error")
	}
}

func TestPriorityOnlyBreaksExactElevationTies(t *testing.T) {
	// equal peak: lower Priority number wins even when it arrives later
	f := fake{map[int][]Pass{1: {pass(60, 10, 40)}}}
	p := newP(f, StrategyPriority)
	p.Generate([]Monitored{mon("low", 1, "rx", 5)})
	f.passes[2] = []Pass{pass(62, 10, 40)}
	p.Generate([]Monitored{mon("low", 1, "rx", 5), mon("high", 2, "rx", 1)})
	for _, o := range p.Timeline() {
		if o.Satellite == "low" && o.Status != StatusCancelled || o.Satellite == "high" && o.Status != StatusScheduled {
			t.Fatalf("tie not broken by priority: %+v", p.Timeline())
		}
	}
	// higher peak beats better priority number
	f = fake{map[int][]Pass{1: {pass(60, 10, 40)}, 2: {pass(62, 10, 41)}}}
	p = newP(f, StrategyPriority)
	p.Generate([]Monitored{mon("prio", 1, "rx", 0), mon("tall", 2, "rx", 9)})
	for _, o := range p.Timeline() {
		if o.Satellite == "tall" && o.Status != StatusScheduled {
			t.Fatalf("priority overrode higher peak: %+v", p.Timeline())
		}
	}
}
