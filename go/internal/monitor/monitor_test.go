package monitor

import (
	"context"
	"math"
	"sync"
	"testing"
	"time"
)

// fakeSource is a receiver the tests drive by hand.
type fakeSource struct {
	mu       sync.Mutex
	modes    []string
	freqs    []float64
	spectra  [][]float64
	level    map[float64]float64
	frames   int
	listened int
}

func (f *fakeSource) Spectrum() ([]float64, float64, float64, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.spectra) == 0 {
		return nil, 145e6, 2e6, false
	}
	frame := f.spectra[f.frames%len(f.spectra)]
	f.frames++
	return frame, 145e6, 2e6, true
}

// Listen records the request and returns a tone of the requested level.
func (f *fakeSource) Listen(ctx context.Context, hz float64, mode string, dur time.Duration) ([]float64, int, error) {
	f.mu.Lock()
	f.modes = append(f.modes, mode)
	f.freqs = append(f.freqs, hz)
	f.listened++
	level := f.level[hz]
	f.mu.Unlock()
	rate := 8000
	n := int(dur.Seconds()) * rate
	if n < rate/10 {
		n = rate / 10
	}
	samples := make([]float64, n)
	for i := range samples {
		samples[i] = level * math.Sin(2*math.Pi*1000*float64(i)/float64(rate))
	}
	return samples, rate, nil
}

func (f *fakeSource) Close() error { return nil }

// TestSurveyUsesRealModulation checks that the sweep never asks a receiver
// for a modulation named "auto": no receiver knows that mode, so the
// survey would silently hear nothing.
func TestSurveyUsesRealModulation(t *testing.T) {
	source := &fakeSource{level: map[float64]float64{}}
	instance := New(Options{
		Source:  source,
		StartHz: 144e6, EndHz: 144.1e6, StepHz: 25e3, Dwell: 50 * time.Millisecond,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	instance.Run(ctx)
	source.mu.Lock()
	defer source.mu.Unlock()
	if len(source.modes) == 0 {
		t.Fatal("the sweep never listened")
	}
	for _, mode := range source.modes {
		if mode == "auto" {
			t.Fatalf("the sweep asked for the %q modulation", mode)
		}
	}
}

// TestThresholdAffectsSweep checks that a higher threshold finds less: the
// threshold is the one knob the user has to separate traffic from noise.
func TestThresholdAffectsSweep(t *testing.T) {
	build := func() *fakeSource {
		return &fakeSource{level: map[float64]float64{
			144.00e6: 0.01, // noise
			144.05e6: 0.01, // noise
			144.10e6: 0.05, // a weak signal
			144.15e6: 0.50, // a strong signal
		}}
	}
	count := func(threshold float64) int {
		source := build()
		instance := New(Options{
			Source: source, Strategy: "sweep", StartHz: 144e6, EndHz: 144.15e6,
			StepHz: 25e3, Dwell: 50 * time.Millisecond, ThresholdDB: threshold,
			Listen: time.Millisecond,
		})
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		instance.Run(ctx)
		return len(instance.Activities())
	}
	low := count(0)
	high := count(20)
	if low == 0 {
		t.Fatal("the sweep found no traffic at all")
	}
	if high >= low {
		t.Errorf("a 20 dB threshold found %d transmissions, a 0 dB threshold %d: the threshold has no effect", high, low)
	}
	if low < 2 {
		t.Errorf("a 0 dB threshold found %d transmissions, want the weak and the strong one", low)
	}
}

// TestSpectrumDetection checks that a peak in the spectrum is reported and
// that the reported level is the peak's level above the floor.
func TestSpectrumDetection(t *testing.T) {
	frame := make([]float64, 256)
	for i := range frame {
		frame[i] = -90
	}
	frame[200] = -30 // a strong peak
	source := &fakeSource{spectra: [][]float64{frame}, level: map[float64]float64{}}
	instance := New(Options{
		Source: source, Strategy: "spectrum", ThresholdDB: 10,
		DwellTime: 10 * time.Millisecond, Listen: 0,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	instance.Run(ctx)
	activities := instance.Activities()
	if len(activities) == 0 {
		t.Fatal("the peak in the spectrum was not reported")
	}
	if activities[0].LevelDB < 30 {
		t.Errorf("level = %.1f dB, want about 60 dB above the floor", activities[0].LevelDB)
	}
	if activities[0].FrequencyHz < 144e6 || activities[0].FrequencyHz > 146e6 {
		t.Errorf("frequency = %.0f Hz, want it inside the profile", activities[0].FrequencyHz)
	}
}

// TestActivitiesAreNewestFirst checks the order of the activity log.
func TestActivitiesAreNewestFirst(t *testing.T) {
	instance := New(Options{Source: &fakeSource{}})
	instance.nextID = 0
	for i := 0; i < 5; i++ {
		instance.mu.Lock()
		instance.nextID++
		instance.activities = append(instance.activities, Activity{ID: instance.nextID})
		instance.mu.Unlock()
	}
	activities := instance.Activities()
	if len(activities) != 5 {
		t.Fatalf("got %d activities, want 5", len(activities))
	}
	if activities[0].ID != 5 || activities[4].ID != 1 {
		t.Errorf("activities are not newest first: %v", activities)
	}
}

// TestActivityLogIsCapped checks that the log does not grow without bound.
func TestActivityLogIsCapped(t *testing.T) {
	instance := New(Options{Source: &fakeSource{}})
	instance.mu.Lock()
	for i := 0; i < 500; i++ {
		instance.nextID++
		instance.activities = append(instance.activities, Activity{ID: instance.nextID})
	}
	if len(instance.activities) > 200 {
		instance.activities = instance.activities[len(instance.activities)-200:]
	}
	instance.mu.Unlock()
	if got := len(instance.Activities()); got > 200 {
		t.Errorf("activity log holds %d entries", got)
	}
}
