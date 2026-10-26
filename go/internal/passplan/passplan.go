// Package passplan builds a persistent, conflict-resolved timeline of
// satellite observations: every pass of every monitored satellite in a
// lookahead window, filtered by peak elevation, trimmed to the part of the
// pass above a task-start elevation, de-duplicated against what is already
// planned, and arbitrated when two observations need the same receiver.
//
// It complements satsched (which only picks the single next pass) by giving
// the UI and API a full multi-pass plan with a status lifecycle.
//
// Design provenance: the behaviour (peak-elevation filter, task window from
// an elevation crossing, +/-5 min overlap tolerance, shared-resource
// conflicts, priority/skip/force strategies, scheduled/running/completed/
// cancelled/failed/missed statuses, regenerate-updates-only-future-scheduled)
// is modelled on the observation scheduler of sgoudelis/ground-station
// (GPL-3.0, pinned 351631d3066b, backend/observations/{constants,conflicts,
// generator}.py). No Python source was copied: this is an independent Go
// implementation of the same rules, licensed AGPL-3.0 with the project.
package passplan

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// Status values of an Observation.
const (
	StatusScheduled = "scheduled"
	StatusRunning   = "running"
	StatusCompleted = "completed"
	StatusCancelled = "cancelled"
	StatusFailed    = "failed"
	StatusMissed    = "missed"
)

// Conflict strategies.
const (
	StrategyPriority = "priority" // higher peak elevation wins
	StrategySkip     = "skip"     // new conflicting pass is dropped
	StrategyForce    = "force"    // schedule anyway
)

// OverlapTolerance widens every window when checking overlap.
const OverlapTolerance = 5 * time.Minute

// Pass is one geometric pass.
type Pass struct {
	AOS, LOS, Peak time.Time
	PeakElevation  float64
}

// Monitored configures one satellite to plan.
type Monitored struct {
	ID                 string
	Name               string
	NoradID            int
	Resource           string  // receiver/SDR id; empty means no resource to clash on
	MinElevation       float64 // peak must reach this (default 20)
	TaskStartElevation float64 // record only above this (default 10; 0 = whole pass)
	LookaheadHours     float64 // default 24
	Priority           int     // sdrstream extension (not upstream): lower number wins ONLY when peak elevations are exactly equal; it never overrides a higher peak
	Enabled            bool
}

// Finder supplies pass geometry. Implementations: TrackerFinder (SGP4) or a
// fake in tests.
type Finder interface {
	// Passes returns passes from horizon to horizon in [from, from+hours].
	Passes(m Monitored, from time.Time, hours float64) ([]Pass, error)
	// Crossing returns when the satellite is above el during the pass:
	// start and end of the sub-window. ok=false if it never reaches el.
	Crossing(m Monitored, p Pass, el float64) (start, end time.Time, ok bool)
}

// Observation is one planned recording.
type Observation struct {
	ID        string    `json:"id"`
	MonID     string    `json:"monitored_id"`
	Satellite string    `json:"satellite"`
	NoradID   int       `json:"norad_id"`
	Resource  string    `json:"resource,omitempty"`
	Priority  int       `json:"priority"`
	AOS       time.Time `json:"aos"`
	LOS       time.Time `json:"los"`
	TaskStart time.Time `json:"task_start"`
	TaskEnd   time.Time `json:"task_end"`
	PeakEl    float64   `json:"peak_elevation"`
	Status    string    `json:"status"`
	Detail    string    `json:"detail,omitempty"`
}

// Stats counts what one Generate call did.
type Stats struct {
	Generated int `json:"generated"`
	Updated   int `json:"updated"`
	Skipped   int `json:"skipped"`
	Replaced  int `json:"replaced"`
	Conflicts int `json:"conflicts"`
}

// Planner holds the timeline.
type Planner struct {
	mu       sync.Mutex
	Strategy string
	Now      func() time.Time
	finder   Finder
	obs      []*Observation
}

// New builds a Planner.
func New(f Finder, strategy string) *Planner {
	if strategy == "" {
		strategy = StrategyPriority
	}
	return &Planner{Strategy: strategy, Now: time.Now, finder: f}
}

func (m Monitored) defaults() Monitored {
	if m.MinElevation <= 0 {
		m.MinElevation = 20
	}
	if m.LookaheadHours <= 0 {
		m.LookaheadHours = 24
	}
	return m
}

func overlaps(aStart, aEnd, bStart, bEnd time.Time) bool {
	return aStart.Before(bEnd.Add(OverlapTolerance)) && aEnd.After(bStart.Add(-OverlapTolerance))
}

// Generate plans passes for every enabled monitored satellite. Higher-priority
// satellites (lower Priority number) are planned first so that, under the
// priority strategy, ties are resolved deterministically.
func (p *Planner) Generate(mons []Monitored) (Stats, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.Now()
	p.markMissed(now)
	var st Stats
	sorted := append([]Monitored(nil), mons...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Priority < sorted[j].Priority })
	var firstErr error
	for _, raw := range sorted {
		if !raw.Enabled {
			continue
		}
		m := raw.defaults()
		passes, err := p.finder.Passes(m, now, m.LookaheadHours)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", m.Name, err)
			}
			continue
		}
		for _, ps := range passes {
			if ps.PeakElevation < m.MinElevation || !ps.LOS.After(now) {
				continue
			}
			p.place(m, ps, now, &st)
		}
	}
	sort.Slice(p.obs, func(i, j int) bool { return p.obs[i].TaskStart.Before(p.obs[j].TaskStart) })
	return st, firstErr
}

func (p *Planner) place(m Monitored, ps Pass, now time.Time, st *Stats) {
	// Same satellite, same pass already planned (within tolerance)?
	for _, o := range p.obs {
		if o.MonID == m.ID && o.NoradID == m.NoradID && overlaps(ps.AOS, ps.LOS, o.AOS, o.LOS) {
			if o.Status == StatusScheduled && o.AOS.After(now) {
				o.AOS, o.LOS, o.PeakEl = ps.AOS, ps.LOS, ps.PeakElevation
				ts, te := p.window(m, ps)
				o.TaskStart, o.TaskEnd = ts, te
				st.Updated++
			} else {
				st.Skipped++
			}
			return
		}
	}
	ts, te := p.window(m, ps)
	cand := &Observation{
		ID:    fmt.Sprintf("%s:%d:%s", m.ID, m.NoradID, ps.AOS.UTC().Format("20060102T150405Z")),
		MonID: m.ID, Satellite: m.Name, NoradID: m.NoradID, Resource: m.Resource,
		Priority: m.Priority, AOS: ps.AOS, LOS: ps.LOS, TaskStart: ts, TaskEnd: te,
		PeakEl: ps.PeakElevation, Status: StatusScheduled,
	}
	if p.Strategy == StrategyForce {
		p.obs = append(p.obs, cand)
		st.Generated++
		return
	}
	var blockers []*Observation
	for _, o := range p.obs {
		if (o.Status == StatusScheduled || o.Status == StatusRunning) && o.Resource != "" &&
			o.Resource == cand.Resource && overlaps(cand.TaskStart, cand.TaskEnd, o.TaskStart, o.TaskEnd) {
			blockers = append(blockers, o)
		}
	}
	if len(blockers) == 0 {
		p.obs = append(p.obs, cand)
		st.Generated++
		return
	}
	st.Conflicts++
	replace := false
	if p.Strategy == StrategyPriority {
		replace = true
		for _, b := range blockers {
			// A running observation is never pre-empted; equal elevation keeps the incumbent.
			win := cand.PeakEl > b.PeakEl ||
				(cand.PeakEl == b.PeakEl && cand.Priority < b.Priority) // tie-break only
			if b.Status == StatusRunning || !win {
				replace = false
			}
		}
	}
	if !replace {
		st.Skipped++
		return
	}
	for _, b := range blockers {
		b.Status = StatusCancelled
		b.Detail = "replaced by higher-elevation pass of " + cand.Satellite
		st.Replaced++
	}
	p.obs = append(p.obs, cand)
	st.Generated++
}

func (p *Planner) window(m Monitored, ps Pass) (time.Time, time.Time) {
	if m.TaskStartElevation > 0 {
		if s, e, ok := p.finder.Crossing(m, ps, m.TaskStartElevation); ok {
			return s, e
		}
	}
	return ps.AOS, ps.LOS
}

func (p *Planner) markMissed(now time.Time) {
	for _, o := range p.obs {
		if o.Status == StatusScheduled && o.TaskEnd.Before(now) {
			o.Status = StatusMissed
		}
	}
}

// Start marks a scheduled observation running (executor hook).
func (p *Planner) Start(id string) error { return p.transition(id, StatusScheduled, StatusRunning, "") }

// Finish marks a running observation completed or failed.
func (p *Planner) Finish(id string, ok bool, detail string) error {
	to := StatusCompleted
	if !ok {
		to = StatusFailed
	}
	return p.transition(id, StatusRunning, to, detail)
}

// Cancel cancels a scheduled observation.
func (p *Planner) Cancel(id string) error {
	return p.transition(id, StatusScheduled, StatusCancelled, "cancelled by user")
}

func (p *Planner) transition(id, from, to, detail string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, o := range p.obs {
		if o.ID == id {
			if o.Status != from {
				return fmt.Errorf("observation %s is %s, not %s", id, o.Status, from)
			}
			o.Status, o.Detail = to, detail
			return nil
		}
	}
	return fmt.Errorf("observation %s not found", id)
}

// Next returns the earliest scheduled observation whose task window has not ended.
func (p *Planner) Next() *Observation {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.Now()
	var best *Observation
	for _, o := range p.obs {
		if o.Status == StatusScheduled && o.TaskEnd.After(now) && (best == nil || o.TaskStart.Before(best.TaskStart)) {
			c := *o
			best = &c
		}
	}
	return best
}

// Timeline returns a copy of all observations ordered by start.
func (p *Planner) Timeline() []Observation {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Observation, 0, len(p.obs))
	for _, o := range p.obs {
		out = append(out, *o)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TaskStart.Before(out[j].TaskStart) })
	return out
}
