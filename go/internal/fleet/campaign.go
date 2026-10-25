package fleet

import (
	"fmt"
	"sort"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/bandcond"
	"github.com/chaim12345/sdrstream/go/internal/hfschedule"
)

// JobsFromOccurrences builds fleet jobs from scheduled HF occurrences:
// this is where campaign work enters the probe -> select -> decode loop.
//
// Mapping: one job per DISTINCT target (deduped by dial frequency + mode;
// duplicate simultaneous windows for the same target want one station,
// not two), keeping the first occurrence's stable identity. Band-condition
// labels annotate the job's label when conds carry one - they are advisory
// (Kp is one input among many) and NEVER filter a job out. Jobs sort with
// better-advised bands first because Planner.Plan takes job order as
// priority. hfschedule predicts airtimes, never reception; this function
// touches no network.
func JobsFromOccurrences(occs []hfschedule.Occurrence, conds bandcond.Conditions, maxAge time.Duration) []Job {
	seen := map[string]bool{}
	jobs := make([]Job, 0, len(occs))
	for _, o := range occs {
		t := o.Entry.Target
		freqHz := int64(t.DialKHz * 1000)
		dedupeKey := fmt.Sprintf("%d/%s", freqHz, t.Mode)
		if seen[dedupeKey] {
			continue
		}
		seen[dedupeKey] = true
		label := t.Name
		if cond, ok := conds.LabelForHz(float64(freqHz)); ok {
			label = fmt.Sprintf("%s [advisory: %s]", t.Name, cond)
		}
		jobs = append(jobs, Job{
			ID:     o.Key(),
			FreqHz: freqHz,
			Mode:   t.Mode,
			Label:  label,
			MaxAge: maxAge,
		})
	}
	rank := func(j Job) int {
		cond, ok := conds.LabelForHz(float64(j.FreqHz))
		if !ok {
			return 3
		}
		switch cond {
		case "good":
			return 0
		case "fair":
			return 1
		case "poor":
			return 2
		}
		return 3
	}
	sort.SliceStable(jobs, func(i, j int) bool { return rank(jobs[i]) < rank(jobs[j]) })
	return jobs
}
