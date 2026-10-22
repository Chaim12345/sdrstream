package capture

import (
	"math"
	"testing"
	"time"
)

func TestAdaptiveHysteresisPreRoll(t *testing.T) {
	d := New(1000, Options{Frame: 20 * time.Millisecond, PreRoll: 100 * time.Millisecond, Hangover: 40 * time.Millisecond, MinRMS: .01, MinFrames: 2})
	at := time.Unix(100, 0)
	var got []Event
	push := func(level float64) {
		x := make([]float64, 20)
		for i := range x {
			x[i] = level * math.Sin(2*math.Pi*100*float64(i)/1000)
		}
		got = append(got, d.Push(x, at)...)
		at = at.Add(20 * time.Millisecond)
	}
	for i := 0; i < 10; i++ {
		push(.001)
	}
	for i := 0; i < 8; i++ {
		push(.1)
	}
	for i := 0; i < 8; i++ {
		push(.001)
	}
	if len(got) != 1 {
		t.Fatalf("events=%d", len(got))
	}
	e := got[0]
	if e.Label != Energy || e.Rate != 1000 || len(e.Samples) < 160 || e.PeakRMS < .05 || e.NoiseFloor <= 0 {
		t.Fatalf("bad event %+v samples=%d", e, len(e.Samples))
	}
	if e.Started.After(time.Unix(100, 0).Add(220 * time.Millisecond)) {
		t.Fatalf("missing onset/preroll: %v", e.Started)
	}
}
func TestNoiseDoesNotOpen(t *testing.T) {
	d := New(1000, Options{Frame: 20 * time.Millisecond, MinRMS: .01})
	at := time.Unix(100, 0)
	for i := 0; i < 100; i++ {
		if es := d.Push(make([]float64, 20), at); len(es) > 0 {
			t.Fatal("silence created event")
		}
		at = at.Add(20 * time.Millisecond)
	}
}
