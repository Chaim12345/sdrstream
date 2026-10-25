// Package fleet turns decode/monitor jobs into station assignments using
// the probe catalog: it is the "select" leg of the probe -> select ->
// decode loop. The planner is pure data-in/data-out: it never dials a
// receiver. Execution (opening a monitor.Source per assignment) is a
// separate, live-gated step owned by the caller.
package fleet

import (
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
type Planner struct {
	// MaxPerStation bounds concurrent assignments per station; receivers
	// have a small number of user slots. Default 1.
	MaxPerStation int

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
	pair := [2]string{jobID, stationKey}
	if ok {
		delete(p.cooldown, pair)
		return
	}
	p.cooldown[pair] = at.Add(failureCooldown)
}

// Plan assigns every placeable job to its best station from the snapshot.
// Passworded stations are always excluded (the fleet holds no credentials);
// busy or dead stations never reach us because Select filters them.
func (p *Planner) Plan(snap probe.Snapshot, jobs []Job, now time.Time) Plan {
	if p.MaxPerStation <= 0 {
		p.MaxPerStation = 1
	}
	if p.last == nil {
		p.last = map[string]string{}
	}
	if p.cooldown == nil {
		p.cooldown = map[[2]string]time.Time{}
	}
	plan := Plan{Assigned: []Assignment{}, Unplaced: []Job{}}
	load := map[string]int{}
	for _, job := range jobs {
		if job.MaxAge <= 0 {
			plan.Unplaced = append(plan.Unplaced, job)
			continue
		}
		req := probe.Requirement{
			FreqHz:     job.FreqHz,
			Kinds:      job.Kinds,
			NoPassword: true,
			MaxAge:     job.MaxAge,
		}
		candidates := probe.Select(snap, req, now, 0)
		var chosen *probe.Record
		prevKey := p.last[job.ID]
		for _, cand := range candidates {
			if load[cand.Key] >= p.MaxPerStation {
				continue
			}
			if until, parked := p.cooldown[[2]string{job.ID, cand.Key}]; parked && now.Before(until) {
				continue
			}
			// Stickiness: keep the previous station when still eligible.
			if cand.Key == prevKey {
				chosen = cand
				break
			}
			if chosen == nil {
				chosen = cand
			}
		}
		if chosen == nil {
			delete(p.last, job.ID)
			plan.Unplaced = append(plan.Unplaced, job)
			continue
		}
		load[chosen.Key]++
		p.last[job.ID] = chosen.Key
		plan.Assigned = append(plan.Assigned, Assignment{Job: job, Station: *chosen})
	}
	return plan
}
