// Package voiceac is an isolated DC-invariant activity diagnostic, not a
// speech classifier or a replacement for the production transcript guard.
package voiceac

import (
	"fmt"
	"math"
	"sort"
)

const Algorithm = "frame_centered_energy_v1"

type Frame struct {
	Lo, Hi              int
	Mean, RawRMS, ACRMS float64
}
type Segment struct{ Lo, Hi int }
type Result struct {
	Algorithm                                     string
	Rate, Samples, FrameSamples                   int
	Floor, StartThreshold, EndThreshold, Coverage float64
	Frames                                        []Frame
	Segments                                      []Segment
}

func finite(x float64) bool { return !math.IsNaN(x) && !math.IsInf(x, 0) }

// Analyze uses per-frame mean-centered RMS, unchanged3x/2x energy ratios,
// .005 absolute start floor,400ms hangover,200ms minimum,150ms padding.
// Frame support is explicit; segment timing deliberately retains the
// production20ms index map, including its fractional-rate mismatch.
// High energy is activity only, never evidence of speech or valid text.
func Analyze(x []float64, rate int) (Result, error) {
	if rate <= 0 || len(x) < rate/4 {
		return Result{}, fmt.Errorf("unsupported source")
	}
	for _, v := range x {
		if !finite(v) {
			return Result{}, fmt.Errorf("nonfinite input")
		}
	}
	n := int(float64(rate) * .02)
	if n < 1 {
		return Result{}, fmt.Errorf("invalid frame")
	}
	r := Result{Algorithm: Algorithm, Rate: rate, Samples: len(x), FrameSamples: n}
	values := []float64{}
	for lo := 0; lo+n <= len(x); lo += n {
		f := Frame{Lo: lo, Hi: lo + n}
		sum, mean := 0., 0.
		// Two passes deliberately avoid unstable E[x^2]-E[x]^2 subtraction.
		for _, v := range x[lo : lo+n] {
			mean += v
		}
		mean /= float64(n)
		ac := 0.
		for _, v := range x[lo : lo+n] {
			sum += v * v
			d := v - mean
			ac += d * d
		}
		f.Mean = mean
		f.RawRMS = math.Sqrt(sum / float64(n))
		f.ACRMS = math.Sqrt(ac / float64(n))
		if !finite(f.Mean) || !finite(f.RawRMS) || !finite(f.ACRMS) {
			return Result{}, fmt.Errorf("nonfinite frame")
		}
		r.Frames = append(r.Frames, f)
		values = append(values, f.ACRMS)
	}
	if len(values) == 0 {
		return Result{}, fmt.Errorf("no frames")
	}
	sort.Float64s(values)
	r.Floor = math.Max(values[len(values)/10], .005/3)
	r.StartThreshold = math.Max(r.Floor*3, .005)
	r.EndThreshold = math.Max(r.Floor*2, .005*.66)
	open, first, last := false, 0, 0
	raw := []Segment{}
	for i, f := range r.Frames {
		switch {
		case !open && f.ACRMS >= r.StartThreshold:
			open, first, last = true, i, i
		case open && f.ACRMS >= r.EndThreshold:
			last = i
		case open && i-last > 20:
			raw = append(raw, Segment{int(float64(first) * .02 * float64(rate)), int(float64(last+1) * .02 * float64(rate))})
			open = false
		}
	}
	if open {
		raw = append(raw, Segment{int(float64(first) * .02 * float64(rate)), int(float64(last+1) * .02 * float64(rate))})
	}
	pad := int(float64(rate) * .15)
	for _, s := range raw {
		if float64(s.Hi-s.Lo)/float64(rate) < .2 {
			continue
		}
		s.Lo -= pad
		if s.Lo < 0 {
			s.Lo = 0
		}
		s.Hi += pad
		if s.Hi > len(x) {
			s.Hi = len(x)
		}
		if k := len(r.Segments); k > 0 && s.Lo <= r.Segments[k-1].Hi {
			r.Segments[k-1].Hi = s.Hi
		} else {
			r.Segments = append(r.Segments, s)
		}
	}
	covered := 0
	for _, s := range r.Segments {
		covered += s.Hi - s.Lo
	}
	r.Coverage = float64(covered) / float64(len(x))
	return r, nil
}
