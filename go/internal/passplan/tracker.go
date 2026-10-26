package passplan

import (
	"time"

	"github.com/chaim12345/sdrstream/go/internal/tracking"
)

// TrackerFinder finds passes with the SGP4 tracker. Trackers maps NORAD id to
// a ready Tracker (loaded from cached CelesTrak elements by the caller).
type TrackerFinder struct {
	Trackers map[int]*tracking.Tracker
	Lat, Lon float64
	AltKm    float64
}

func (f TrackerFinder) Passes(m Monitored, from time.Time, hours float64) ([]Pass, error) {
	tr, ok := f.Trackers[m.NoradID]
	if !ok {
		return nil, errNoTracker(m.NoradID)
	}
	ps, err := tr.FindPasses(f.Lat, f.Lon, from, hours, 0)
	if err != nil {
		return nil, err
	}
	out := make([]Pass, 0, len(ps))
	for _, p := range ps {
		out = append(out, Pass{AOS: p.Rise, LOS: p.Set, Peak: p.Peak.Time, PeakElevation: p.MaxElevation()})
	}
	return out, nil
}

// Crossing bisects the rising and setting elevation crossings around the peak.
func (f TrackerFinder) Crossing(m Monitored, p Pass, el float64) (time.Time, time.Time, bool) {
	tr, ok := f.Trackers[m.NoradID]
	if !ok || p.PeakElevation < el {
		return time.Time{}, time.Time{}, false
	}
	elAt := func(t time.Time) float64 {
		la, err := tr.LookAngle(t, f.Lat, f.Lon, f.AltKm)
		if err != nil {
			return -90
		}
		return la.Elevation
	}
	bisect := func(lo, hi time.Time, rising bool) time.Time {
		// invariant: below el at lo side, above el at hi side (swapped when setting)
		for i := 0; i < 30 && hi.Sub(lo) > time.Second; i++ {
			mid := lo.Add(hi.Sub(lo) / 2)
			above := elAt(mid) >= el
			if above == rising {
				hi = mid
			} else {
				lo = mid
			}
		}
		if rising {
			return hi
		}
		return lo
	}
	return bisect(p.AOS, p.Peak, true), bisect(p.Peak, p.LOS, false), true
}

type errNoTracker int

func (e errNoTracker) Error() string { return "no orbital elements loaded for NORAD " + itoa(int(e)) }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for ; n > 0; n /= 10 {
		s = string(rune('0'+n%10)) + s
	}
	return s
}
