// Package capture provides a streaming adaptive PCM activity detector with
// bounded pre-roll. Acoustic labels are evidence only; decoders must pass an
// independent verifier before a capture can be labeled Decoded.
package capture

import (
	"math"
	"sort"
	"time"
)

type Label string

const (
	Energy  Label = "energy"
	Speech  Label = "speech"
	Decoded Label = "decoded"
)

type Options struct {
	Frame       time.Duration
	PreRoll     time.Duration
	Hangover    time.Duration
	MinRMS      float64
	StartRatio  float64
	EndRatio    float64
	MinFrames   int
	NoiseWindow time.Duration
}

func (o Options) defaults() Options {
	if o.Frame <= 0 {
		o.Frame = 20 * time.Millisecond
	}
	if o.PreRoll <= 0 {
		o.PreRoll = time.Second
	}
	if o.Hangover <= 0 {
		o.Hangover = 400 * time.Millisecond
	}
	if o.MinRMS <= 0 {
		o.MinRMS = .003
	}
	if o.StartRatio <= 0 {
		o.StartRatio = 3
	}
	if o.EndRatio <= 0 {
		o.EndRatio = 2
	}
	if o.MinFrames <= 0 {
		o.MinFrames = 3
	}
	if o.NoiseWindow <= 0 {
		o.NoiseWindow = 10 * time.Second
	}
	return o
}

type Event struct {
	Samples             []float64
	Rate                int
	Started, Ended      time.Time
	PeakRMS, NoiseFloor float64
	Label               Label
}
type frame struct {
	samples []float64
	rms     float64
	at      time.Time
}
type Detector struct {
	o                 Options
	rate              int
	pending           []float64
	floorFrames       []float64
	pre               []frame
	active            []float64
	opened, lastAbove time.Time
	peak, floor       float64
	above             int
	nextFrame         time.Time
}

func New(rate int, o Options) *Detector { return &Detector{o: o.defaults(), rate: rate} }
func (d *Detector) NoiseFloor() float64 { return d.floor }

// Push accepts the samples' first-sample time. Fragmentation across calls is
// safe: frame timestamps are generated from a carried sample clock.
func (d *Detector) Push(samples []float64, at time.Time) []Event {
	if d.rate <= 0 || len(samples) == 0 {
		return nil
	}
	if at.IsZero() {
		at = time.Now()
	}
	if len(d.pending) == 0 {
		d.nextFrame = at
	}
	d.pending = append(d.pending, samples...)
	fn := int(math.Round(float64(d.rate) * d.o.Frame.Seconds()))
	if fn < 1 {
		return nil
	}
	var out []Event
	for len(d.pending) >= fn {
		block := append([]float64(nil), d.pending[:fn]...)
		d.pending = d.pending[fn:]
		f := frame{samples: block, rms: rms(block), at: d.nextFrame}
		d.nextFrame = d.nextFrame.Add(time.Duration(fn) * time.Second / time.Duration(d.rate))
		out = append(out, d.consume(f)...)
	}
	return out
}
func (d *Detector) consume(f frame) []Event {
	n := max(1, int(float64(d.rate)*d.o.NoiseWindow.Seconds()/float64(len(f.samples))))
	if !d.open() {
		d.floorFrames = append(d.floorFrames, f.rms)
		if len(d.floorFrames) > n {
			d.floorFrames = append([]float64(nil), d.floorFrames[len(d.floorFrames)-n:]...)
		}
		v := append([]float64(nil), d.floorFrames...)
		sort.Float64s(v)
		if len(v) > 0 {
			d.floor = v[len(v)/10]
			if d.floor < d.o.MinRMS/3 {
				d.floor = d.o.MinRMS / 3
			}
		}
	}
	start := math.Max(d.o.MinRMS, d.floor*d.o.StartRatio)
	end := math.Max(d.o.MinRMS*.66, d.floor*d.o.EndRatio)
	if !d.open() {
		d.pre = append(d.pre, f)
		pn := max(1, int(float64(d.rate)*d.o.PreRoll.Seconds()/float64(len(f.samples))))
		if len(d.pre) > pn {
			d.pre = append([]frame(nil), d.pre[len(d.pre)-pn:]...)
		}
		if f.rms >= start {
			d.above++
		} else {
			d.above = 0
		}
		if d.above < d.o.MinFrames {
			return nil
		}
		first := d.pre[len(d.pre)-d.above]
		d.opened = first.at
		d.lastAbove = f.at
		d.peak = f.rms
		d.active = nil
		for _, p := range d.pre {
			d.active = append(d.active, p.samples...)
		}
		return nil
	}
	d.active = append(d.active, f.samples...)
	if f.rms > d.peak {
		d.peak = f.rms
	}
	if f.rms >= end {
		d.lastAbove = f.at
		return nil
	}
	if f.at.Sub(d.lastAbove) < d.o.Hangover {
		return nil
	}
	e := Event{Samples: d.active, Rate: d.rate, Started: d.opened, Ended: f.at, PeakRMS: d.peak, NoiseFloor: d.floor, Label: Energy}
	d.active = nil
	d.above = 0
	d.peak = 0
	return []Event{e}
}
func (d *Detector) open() bool { return d.active != nil }
func rms(x []float64) float64 {
	var sum float64
	for _, v := range x {
		sum += v * v
	}
	if len(x) == 0 {
		return 0
	}
	return math.Sqrt(sum / float64(len(x)))
}

// SpeechLabel upgrades acoustic activity only when a VAD finds voice.
func SpeechLabel(e Event, detect func([]float64, int) bool) Event {
	if e.Label == Energy && detect != nil && detect(e.Samples, e.Rate) {
		e.Label = Speech
	}
	return e
}
