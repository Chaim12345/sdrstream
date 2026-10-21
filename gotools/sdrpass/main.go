// Command sdrpass predicts satellite passes for many observers at once.
//
// It is the fast path behind tools/record_sky.py. The Python pass finder is
// correct but pure-Python: an SGP4 run costs tens of microseconds, and a sweep
// over hundreds of receivers multiplies that by the receiver count. Here the
// orbit is propagated once per time step and every observer is evaluated from
// that one position, so the receiver count only costs a cheap look-angle
// conversion each.
//
// It reads a job as JSON on stdin and writes the passes as JSON on stdout, so
// the caller keeps ownership of the satellite table and the receiver list.
//
// The orbit model is the same akhenakh/sgp4 library the main Go module uses,
// so pass predictions cannot drift between the two code paths.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	sgp4 "github.com/akhenakh/sgp4"
)

// Job is the request read from stdin.
type Job struct {
	Start        string        `json:"start"`
	Hours        float64       `json:"hours"`
	MinElevation float64       `json:"min_elevation"`
	StepSeconds  float64       `json:"step_seconds"`
	Satellites   []JobSat      `json:"satellites"`
	Observers    []JobObserver `json:"observers"`
}

// JobSat is one satellite to predict.
type JobSat struct {
	Name  string `json:"name"`
	Line1 string `json:"line1"`
	Line2 string `json:"line2"`
}

// JobObserver is one ground station.
type JobObserver struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

// PassOut is one predicted pass, times as RFC3339 in UTC.
type PassOut struct {
	Rise         string  `json:"rise"`
	Set          string  `json:"set"`
	Peak         string  `json:"peak"`
	MaxElevation float64 `json:"max_elevation"`
	Duration     float64 `json:"duration_seconds"`
}

// SatResult holds one pass list per observer, in the input order.
type SatResult struct {
	Name      string      `json:"name"`
	Observers [][]PassOut `json:"observers"`
}

// Report is the whole response.
type Report struct {
	Satellites []SatResult `json:"satellites"`
	ElapsedMs  float64     `json:"elapsed_ms"`
}

// satelliteState carries one satellite's propagation plus the per-observer
// look-angle machinery built once per satellite.
type satelliteState struct {
	tle       *sgp4.TLE
	observers []*sgp4.Location
}

// elevation returns the elevation in degrees above observer index i at t.
func (s *satelliteState) elevation(i int, t time.Time) (float64, error) {
	eci, err := s.tle.FindPositionAtTime(t)
	if err != nil {
		return 0, err
	}
	state := &sgp4.StateVector{
		X: eci.Position.X, Y: eci.Position.Y, Z: eci.Position.Z,
		VX: eci.Velocity.X, VY: eci.Velocity.Y, VZ: eci.Velocity.Z,
	}
	observation, err := state.GetLookAngle(s.observers[i], t)
	if err != nil {
		return 0, err
	}
	return observation.LookAngles.Elevation, nil
}

// sample is one coarse elevation reading within a pass.
type sample struct {
	at time.Time
	el float64
}

func main() {
	flag.Parse()

	var job Job
	if err := json.NewDecoder(os.Stdin).Decode(&job); err != nil {
		fail("reading job: %v", err)
	}
	if len(job.Satellites) == 0 || len(job.Observers) == 0 {
		fail("job needs at least one satellite and one observer")
	}
	if job.StepSeconds <= 0 {
		job.StepSeconds = 30.0
	}
	start, err := time.Parse(time.RFC3339, job.Start)
	if err != nil {
		fail("bad start time %q: %v", job.Start, err)
	}
	start = start.UTC()

	began := time.Now()
	report := Report{Satellites: make([]SatResult, 0, len(job.Satellites))}
	for _, js := range job.Satellites {
		result := SatResult{
			Name:      js.Name,
			Observers: make([][]PassOut, len(job.Observers)),
		}
		tle, err := sgp4.ParseTLELines([]string{js.Line1, js.Line2})
		if err != nil {
			fail("satellite %s: %v", js.Name, err)
		}
		state := &satelliteState{tle: tle}
		for _, obs := range job.Observers {
			state.observers = append(state.observers, &sgp4.Location{
				Latitude: obs.Lat, Longitude: obs.Lon, Altitude: 0,
			})
		}
		for i := range job.Observers {
			result.Observers[i] = findPasses(
				state, i, start, job.Hours,
				job.MinElevation, job.StepSeconds)
		}
		report.Satellites = append(report.Satellites, result)
	}
	report.ElapsedMs = float64(time.Since(began).Microseconds()) / 1000.0

	out := json.NewEncoder(os.Stdout)
	out.SetIndent("", "  ")
	if err := out.Encode(report); err != nil {
		fail("writing report: %v", err)
	}
}

func fail(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "sdrpass: "+format+"\n", args...)
	os.Exit(1)
}

// findPasses scans coarse then refines the rise, peak and set of each pass.
func findPasses(sat *satelliteState, observer int, start time.Time,
	hours, minElevation, stepSeconds float64) []PassOut {

	step := time.Duration(stepSeconds * float64(time.Second))
	end := start.Add(time.Duration(hours * float64(time.Hour)))

	passes := []PassOut{}
	var riseTime *time.Time
	samples := []sample{}

	for t := start; !t.After(end); t = t.Add(step) {
		el, err := sat.elevation(observer, t)
		if err != nil {
			continue // a step that cannot propagate is skipped, not fatal
		}
		if el > 0 && riseTime == nil {
			r := refineCrossing(sat, observer, t.Add(-step), t, true)
			riseTime = &r
			samples = samples[:0]
		}
		if el > 0 {
			samples = append(samples, sample{t, el})
			continue
		}
		if riseTime == nil {
			continue
		}
		setTime := refineCrossing(sat, observer, t.Add(-step), t, false)
		peakAt, peakEl := refinePeak(sat, observer, samples)
		if peakEl >= minElevation {
			passes = append(passes, PassOut{
				Rise:         riseTime.UTC().Format(time.RFC3339),
				Set:          setTime.UTC().Format(time.RFC3339),
				Peak:         peakAt.UTC().Format(time.RFC3339),
				MaxElevation: peakEl,
				Duration:     setTime.Sub(*riseTime).Seconds(),
			})
		}
		riseTime = nil
		samples = samples[:0]
	}
	return passes
}

// refineCrossing bisects to the minute a horizon crossing between two times.
func refineCrossing(sat *satelliteState, observer int, before, after time.Time,
	rising bool) time.Time {
	lo, hi := before, after
	for i := 0; i < 12; i++ {
		mid := lo.Add(hi.Sub(lo) / 2)
		el, err := sat.elevation(observer, mid)
		if err != nil {
			break
		}
		if (el > 0) == rising {
			hi = mid
		} else {
			lo = mid
		}
	}
	return hi
}

// refinePeak golden-section searches the sample with the highest elevation.
func refinePeak(sat *satelliteState, observer int, samples []sample) (time.Time, float64) {
	if len(samples) == 0 {
		return time.Time{}, 0
	}
	bestAt := samples[0].at
	bestEl := samples[0].el
	for _, s := range samples[1:] {
		if s.el > bestEl {
			bestAt, bestEl = s.at, s.el
		}
	}
	// Walk downhill on both sides with a shrinking step to sharpen the peak.
	step := time.Minute
	for i := 0; i < 40; i++ {
		improved := false
		for _, dir := range []time.Duration{-step, step} {
			candidate := bestAt.Add(dir)
			el, err := sat.elevation(observer, candidate)
			if err == nil && el > bestEl {
				bestAt, bestEl = candidate, el
				improved = true
			}
		}
		if !improved {
			step /= 2
			if step < time.Second {
				break
			}
		}
	}
	return bestAt, bestEl
}
