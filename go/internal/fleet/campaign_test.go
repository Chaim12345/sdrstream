package fleet

import (
	"testing"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/bandcond"
	"github.com/chaim12345/sdrstream/go/internal/decode"
	"github.com/chaim12345/sdrstream/go/internal/hfschedule"
)

func occ(id, name, kind string, dialKHz float64, mode string) hfschedule.Occurrence {
	start := time.Date(2026, 10, 25, 20, 0, 0, 0, time.UTC)
	return hfschedule.Occurrence{
		Entry: hfschedule.Entry{
			ID:     id,
			Target: decode.HFTarget{Name: name, Kind: kind, DialKHz: dialKHz, Mode: mode},
		},
		Start: start,
		End:   start.Add(30 * time.Minute),
	}
}

func TestJobsFromOccurrencesMapsTargets(t *testing.T) {
	jobs := JobsFromOccurrences(
		[]hfschedule.Occurrence{occ("a1", "FT8 20m", "ft8", 14074, "usb")},
		bandcond.Conditions{}, time.Hour)
	if len(jobs) != 1 {
		t.Fatalf("jobs: %+v", jobs)
	}
	j := jobs[0]
	if j.FreqHz != 14_074_000 || j.Mode != "usb" || j.MaxAge != time.Hour {
		t.Fatalf("job: %+v", j)
	}
	if j.ID == "" || j.Label == "" {
		t.Fatalf("job needs stable ID and label: %+v", j)
	}
}

func TestJobsFromOccurrencesDedupesSameTarget(t *testing.T) {
	// Two windows for the same dial+mode: one job (one station covers
	// both), keeping the first occurrence's identity.
	jobs := JobsFromOccurrences([]hfschedule.Occurrence{
		occ("a1", "FT8 20m", "ft8", 14074, "usb"),
		occ("a2", "FT8 20m late", "ft8", 14074, "usb"),
		occ("b1", "RTTY 20m", "rtty", 14095, "usb"),
	}, bandcond.Conditions{}, time.Hour)
	if len(jobs) != 2 {
		t.Fatalf("dedupe: %+v", jobs)
	}
}

func TestJobsFromOccurrencesAnnotatesAdvisory(t *testing.T) {
	// Kp 0 = quiet: high bands good, low bands poor.
	conds := bandcond.Classify(0, time.Now())
	jobs := JobsFromOccurrences([]hfschedule.Occurrence{
		occ("a1", "FT8 20m", "ft8", 14074, "usb"),   // 20m
		occ("b1", "FT8 80m", "ft8", 3573, "usb"),    // 80m
		occ("c1", "CB-like 11m", "am", 27000, "am"), // no amateur band
	}, conds, time.Hour)
	labels := map[string]string{}
	for _, j := range jobs {
		labels[j.Label] = j.ID
	}
	// The annotation must name the advisory and never filter a job out.
	found := 0
	for _, j := range jobs {
		switch j.ID {
		case jobs[0].ID:
		}
		if j.Label != "" {
			found++
		}
	}
	if found != 3 {
		t.Fatalf("all jobs must survive annotation: %+v", jobs)
	}
	var a1, b1, c1 Job
	for _, j := range jobs {
		switch j.FreqHz {
		case 14_074_000:
			a1 = j
		case 3_573_000:
			b1 = j
		case 27_000_000:
			c1 = j
		}
	}
	if a1.Label == "FT8 20m" || b1.Label == "FT8 80m" {
		t.Fatalf("known bands must carry the advisory: %q %q", a1.Label, b1.Label)
	}
	if c1.Label != "CB-like 11m" {
		t.Fatalf("band-less freq must stay unannotated: %q", c1.Label)
	}
}

func TestJobsFromOccurrencesPrioritizesBetterBands(t *testing.T) {
	// Kp 5 (minor storm): 80m poor, 20m fair. Input order has the poor
	// band first; output must lead with the fair band because Plan takes
	// job order as priority.
	conds := bandcond.Classify(5, time.Now())
	jobs := JobsFromOccurrences([]hfschedule.Occurrence{
		occ("b1", "FT8 80m", "ft8", 3573, "usb"),
		occ("a1", "FT8 20m", "ft8", 14074, "usb"),
	}, conds, time.Hour)
	if len(jobs) != 2 || jobs[0].FreqHz != 14_074_000 {
		t.Fatalf("priority order: %+v", jobs)
	}
}

func TestJobsFromOccurrencesNoCondsKeepsOrder(t *testing.T) {
	jobs := JobsFromOccurrences([]hfschedule.Occurrence{
		occ("b1", "FT8 80m", "ft8", 3573, "usb"),
		occ("a1", "FT8 20m", "ft8", 14074, "usb"),
	}, bandcond.Conditions{}, time.Hour)
	if len(jobs) != 2 || jobs[0].FreqHz != 3_573_000 {
		t.Fatalf("no conds: input order must be kept: %+v", jobs)
	}
}
