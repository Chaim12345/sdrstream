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
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	satellite "github.com/Mohammed-Ashour/go-satellite-v2/pkg/satellite"
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
		sat := satellite.TLEToSat(js.Line1, js.Line2, satellite.GravityWGS72)
		result := SatResult{
			Name:      js.Name,
			Observers: make([][]PassOut, len(job.Observers)),
		}
		for i := range job.Observers {
			result.Observers[i] = findPasses(
				sat, job.Observers[i], start, job.Hours,
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

// elevation returns the elevation in degrees of sat above obs at t.
func elevation(sat satellite.Satellite, obs satellite.LatLong, t time.Time) float64 {
	position, _ := satellite.Propagate(
		sat, t.Year(), int(t.Month()), t.Day(), t.Hour(), t.Minute(), t.Second())
	jd := satellite.JDay(
		t.Year(), int(t.Month()), t.Day(), t.Hour(), t.Minute(), t.Second())
	look := satellite.ECIToLookAngles(position, obs, 0.0, jd)
	return look.El * satellite.RAD2DEG
}

// findPasses scans coarse then refines the rise, peak and set of each pass.
func findPasses(sat satellite.Satellite, obs JobObserver, start time.Time,
	hours, minElevation, stepSeconds float64) []PassOut {

	obsRad := satellite.LatLong{
		Latitude:  obs.Lat * satellite.DEG2RAD,
		Longitude: obs.Lon * satellite.DEG2RAD,
	}
	step := time.Duration(stepSeconds * float64(time.Second))
	end := start.Add(time.Duration(hours * float64(time.Hour)))

	passes := []PassOut{}
	var riseTime *time.Time
	samples := []sample{}

	for t := start; !t.After(end); t = t.Add(step) {
		el := elevation(sat, obsRad, t)
		if el > 0 && riseTime == nil {
			r := refineCrossing(sat, obsRad, t.Add(-step), t, true)
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
		setTime := refineCrossing(sat, obsRad, t.Add(-step), t, false)
		peakAt, peakEl := refinePeak(sat, obsRad, samples)
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

// refineCrossing bisects between before and after to the horizon crossing.
func refineCrossing(sat satellite.Satellite, obs satellite.LatLong,
	before, after time.Time, rising bool) time.Time {

	lo, hi := before, after
	for i := 0; i < 12; i++ {
		mid := lo.Add(hi.Sub(lo) / 2)
		above := elevation(sat, obs, mid) > 0
		if above == rising {
			hi = mid
		} else {
			lo = mid
		}
	}
	return lo.Add(hi.Sub(lo) / 2)
}

// refinePeak ternary-searches the apex between the samples either side of the
// tallest one; elevation is unimodal across a single pass.
func refinePeak(sat satellite.Satellite, obs satellite.LatLong,
	samples []sample) (time.Time, float64) {

	best := 0
	for i := range samples {
		if samples[i].el > samples[best].el {
			best = i
		}
	}
	lo := samples[best].at
	hi := samples[best].at
	if best > 0 {
		lo = samples[best-1].at
	}
	if best+1 < len(samples) {
		hi = samples[best+1].at
	}
	if !lo.Before(hi) {
		return samples[best].at, samples[best].el
	}
	for i := 0; i < 25; i++ {
		a := lo.Add(hi.Sub(lo) / 3)
		b := hi.Add(hi.Sub(lo) / -3)
		if elevation(sat, obs, a) < elevation(sat, obs, b) {
			lo = a
		} else {
			hi = b
		}
	}
	mid := lo.Add(hi.Sub(lo) / 2)
	return mid, elevation(sat, obs, mid)
}
