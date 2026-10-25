// Package fleet turns decode/monitor jobs into station assignments using
// the probe catalog: it is the "select" leg of the probe -> select ->
// decode loop. The planner is pure data-in/data-out: it never dials a
// receiver. Execution (opening a monitor.Source per assignment) is a
// separate, live-gated step owned by the caller.
package fleet

import (
	"sync"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/probe"
)

// failureCooldown is how long a (job, station) pair is avoided after a
// failed outcome before it becomes eligible again.
const failureCooldown = 30 * time.Minute

// Job is one decode/listen task to place on a station.
type Job struct {
	ID     string
	FreqHz int64  // centre frequency the job needs to hear
	Mode   string // modulation, e.g. "usb", "nfm"
	Label  string // human-readable, e.g. "FT8 20m"
	// MaxAge bounds how fresh a station's probe answer must be. Required:
	// a job with zero MaxAge is never placed, because assigning immediate
	// work from week-old probe data is exactly the footgun QA flagged.
	MaxAge time.Duration
	// Kinds, when non-empty, restricts candidate receiver kinds.
	Kinds []string
}

// Assignment pairs a job with the chosen station. The Station record
// carries Kind and URL - everything an executor needs to dial.
type Assignment struct {
	Job     Job
	Station probe.Record
}

// Plan is the result of one planning pass: every job is either assigned
// or explicitly reported unplaced (no station matched).
type Plan struct {
	Assigned []Assignment
	Unplaced []Job
}

// Planner assigns jobs to catalog stations. It remembers previous
// assignments (stickiness: retuning a healthy station is wasted work)
// and recent failures (a failed (job, station) pair cools down).
// The zero value is unusable; NewPlanner applies defaults.
//
// Safe for concurrent use: the executor calls RecordOutcome from worker
// goroutines while the next Plan runs (QA review).
type Planner struct {
	// MaxPerStation bounds concurrent assignments per station; receivers
	// have a small number of user slots. Default 1.
	MaxPerStation int

	mu       sync.Mutex
	last     map[string]string       // job ID -> station key
	cooldown map[[2]string]time.Time // (job, station) -> eligible again at
}

// NewPlanner returns a Planner with safe defaults.
func NewPlanner() *Planner {
	return &Planner{
		MaxPerStation: 1,
		last:          map[string]string{},
		cooldown:      map[[2]string]time.Time{},
	}
}

// RecordOutcome feeds an assignment result back: a failure parks the
// (job, station) pair for failureCooldown; a success clears any park.
func (p *Planner) RecordOutcome(jobID, stationKey string, ok bool, at time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.init()
	pair := [2]string{jobID, stationKey}
	if ok {
		delete(p.cooldown, pair)
		return
	}
	p.cooldown[pair] = at.Add(failureCooldown)
}

func (p *Planner) Plan(snap probe.Snapshot, jobs []Job, now time.Time) Plan {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.init()
	if p.MaxPerStation <= 0 {
		p.MaxPerStation = 1
	}
	// Prune expired cooldown entries so the map stays bounded by the
	// number of live parks, not by total historical failures.
	for pair, until := range p.cooldown {
		if !now.Before(until) {
			delete(p.cooldown, pair)
		}
	}
	plan := Plan{Assigned: []Assignment{}, Unplaced: []Job{}}
	load := map[string]int{}
	placed := make([]bool, len(jobs))

	// eligible reports whether cand may take job: under capacity and not
	// in failure cooldown for this job.
	eligible := func(job Job, cand *probe.Record) bool {
		if load[cand.Key] >= p.MaxPerStation {
			return false
		}
		if until, parked := p.cooldown[[2]string{job.ID, cand.Key}]; parked && now.Before(until) {
			return false
		}
		return true
	}
	candidatesFor := func(job Job) []*probe.Record {
		return probe.Select(snap, probe.Requirement{
			FreqHz:     job.FreqHz,
			Kinds:      job.Kinds,
			NoPassword: true,
			MaxAge:     job.MaxAge,
		}, now, 0)
	}
	assign := func(idx int, rec *probe.Record) {
		load[rec.Key]++
		p.last[jobs[idx].ID] = rec.Key
		plan.Assigned = append(plan.Assigned, Assignment{Job: jobs[idx], Station: *rec})
		placed[idx] = true
	}

	// Pass 1 - stickiness: jobs keep their previous station when it is
	// still eligible. This runs before any open placement so an earlier
	// non-sticky job cannot steal a later job's station (QA review).
	for i, job := range jobs {
		if job.MaxAge <= 0 {
			continue
		}
		prevKey := p.last[job.ID]
		if prevKey == "" {
			continue
		}
		for _, cand := range candidatesFor(job) {
			if cand.Key == prevKey && eligible(job, cand) {
				assign(i, cand)
				break
			}
		}
	}
	// Pass 2 - open placement in job order (priority order).
	for i, job := range jobs {
		if placed[i] {
			continue
		}
		if job.MaxAge <= 0 {
			// Assignment without a staleness bound is refused (QA footgun).
			plan.Unplaced = append(plan.Unplaced, job)
			continue
		}
		var chosen *probe.Record
		for _, cand := range candidatesFor(job) {
			if eligible(job, cand) {
				chosen = cand
				break
			}
		}
		if chosen == nil {
			delete(p.last, job.ID)
			plan.Unplaced = append(plan.Unplaced, job)
			continue
		}
		assign(i, chosen)
	}
	return plan
}

func (p *Planner) init() {
	if p.last == nil {
		p.last = map[string]string{}
	}
	if p.cooldown == nil {
		p.cooldown = map[[2]string]time.Time{}
	}
}

// cooldownSize reports the live cooldown entries; tests only.
func (p *Planner) cooldownSize() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.cooldown)
}
