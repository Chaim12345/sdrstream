package monitor

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/kiwisdr"
)

// panoSource is a Kiwi-panorama-shaped fake: 1024 bins over 0-30 MHz,
// plus a refinement window for the zoom-in flow.
type panoSource struct {
	mu           sync.Mutex
	frames       [][]float64
	idx          int
	refineBins   []float64
	refineCentre float64
	refineSpan   float64
	refineCalls  []float64
}

func (p *panoSource) Spectrum() ([]float64, float64, float64, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.frames) == 0 {
		return nil, 0, 0, false
	}
	frame := p.frames[p.idx%len(p.frames)]
	p.idx++
	return frame, 15e6, 30e6, true
}

func (p *panoSource) RefineSpectrum(ctx context.Context, freqHz float64) ([]float64, float64, float64, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.refineCalls = append(p.refineCalls, freqHz)
	if len(p.refineBins) == 0 {
		return nil, 0, 0, false
	}
	return p.refineBins, p.refineCentre, p.refineSpan, true
}

func (p *panoSource) Listen(ctx context.Context, hz float64, mode string, dur time.Duration) ([]float64, int, error) {
	return nil, 0, errors.New("not implemented")
}
func (p *panoSource) Close() error { return nil }

// panoFrame builds a 1024-bin frame at floor with one peak at bin.
func panoFrame(floor, peak float64, bin int) []float64 {
	frame := make([]float64, 1024)
	for i := range frame {
		frame[i] = floor
	}
	frame[bin] = floor + peak
	return frame
}

// TestStepAveragingSuppressesOneFrameSpike: a spike present in only one
// of three frames averages below the start level and must not create a
// candidate; the same level present in every frame must.
func TestStepAveragingSuppressesOneFrameSpike(t *testing.T) {
	ctx := context.Background()
	spike := panoFrame(-105, 27, 300)
	quiet := panoFrame(-105, 0, 300)

	transient := New(Options{
		Source: &panoSource{frames: [][]float64{spike, quiet, quiet}},
		ThresholdDB: 10, AverageFrames: 3, DwellTime: time.Millisecond,
	})
	for i := 0; i < 6; i++ {
		transient.step(ctx)
	}
	transient.mu.Lock()
	got := len(transient.active)
	transient.mu.Unlock()
	if got != 0 {
		t.Errorf("one-frame spike created %d candidates with averaging on", got)
	}

	steady := New(Options{
		Source: &panoSource{frames: [][]float64{spike, spike, spike}},
		ThresholdDB: 10, AverageFrames: 3, DwellTime: time.Millisecond,
	})
	for i := 0; i < 3; i++ {
		steady.step(ctx)
	}
	steady.mu.Lock()
	got = len(steady.active)
	steady.mu.Unlock()
	if got == 0 {
		t.Error("steady signal lost by averaging")
	}
}

// TestStepHysteresis: a candidate stays alive on the lower stop level
// but is only reported while it actually holds the start level.
func TestStepHysteresis(t *testing.T) {
	ctx := context.Background()
	strong := panoFrame(-105, 20, 400)
	medium := panoFrame(-105, 8, 400) // above stop (10-3), below start (10)
	source := &panoSource{frames: [][]float64{strong}}
	var activities []Activity
	var mu sync.Mutex
	instance := New(Options{
		Source: source, ThresholdDB: 10, HysteresisDB: 3, DwellTime: time.Millisecond,
		OnActivity: func(a Activity) { mu.Lock(); activities = append(activities, a); mu.Unlock() },
	})
	instance.step(ctx) // creates the candidate at start level
	time.Sleep(2 * time.Millisecond)
	source.mu.Lock()
	source.frames = [][]float64{medium}
	source.mu.Unlock()
	instance.step(ctx) // refreshes on stop level; dwell has passed but start level is gone
	mu.Lock()
	if len(activities) != 0 {
		t.Fatalf("reported %d activities on stop-level energy", len(activities))
	}
	mu.Unlock()
	instance.mu.Lock()
	if len(instance.active) != 1 {
		t.Fatalf("candidate lost on stop level: %d active", len(instance.active))
	}
	instance.mu.Unlock()
	source.mu.Lock()
	source.frames = [][]float64{strong}
	source.mu.Unlock()
	instance.step(ctx) // back at start level: now it reports
	mu.Lock()
	defer mu.Unlock()
	if len(activities) != 1 {
		t.Fatalf("start-level return reported %d activities, want 1", len(activities))
	}
}

// TestStepRefineCoarsePanorama: a coarse panorama detection is zoomed in
// before reporting; the activity carries the refined frequency.
func TestStepRefineCoarsePanorama(t *testing.T) {
	ctx := context.Background()
	const binHz = 30e6 / 1024
	pano := panoFrame(-105, 30, 246)
	coarseFreq := (246 + 0.5) * binHz
	// Fine window: ~915 Hz bins centred on the coarse neighbourhood, peak
	// in the middle at a frequency the panorama cannot resolve.
	const fineBinHz = 30e6 / (1024 * 32)
	fine := make([]float64, 1024)
	for i := range fine {
		fine[i] = -105
	}
	fine[512] = -75
	fineCentre := coarseFreq
	fineSpan := 1024 * fineBinHz
	refinedFreq := fineCentre - fineSpan/2 + (512+0.5)*fineBinHz

	source := &panoSource{
		frames:       [][]float64{pano},
		refineBins:   fine,
		refineCentre: fineCentre,
		refineSpan:   fineSpan,
	}
	var activities []Activity
	var mu sync.Mutex
	instance := New(Options{
		Source: source, ThresholdDB: 10, DwellTime: time.Millisecond,
		OnActivity: func(a Activity) { mu.Lock(); activities = append(activities, a); mu.Unlock() },
	})
	instance.step(ctx)          // candidate
	time.Sleep(2 * time.Millisecond)
	instance.step(ctx)          // dwell passed: refine, then report
	source.mu.Lock()
	if len(source.refineCalls) != 1 {
		t.Fatalf("refine calls %v, want exactly 1", source.refineCalls)
	}
	if got := source.refineCalls[0]; got < coarseFreq-binHz || got > coarseFreq+binHz {
		t.Errorf("refine target %.0f Hz, want near %.0f Hz", got, coarseFreq)
	}
	source.mu.Unlock()
	instance.mu.Lock()
	var cand *candidate
	for _, c := range instance.active {
		cand = c
	}
	instance.mu.Unlock()
	if cand == nil || !cand.refined {
		t.Fatal("candidate not refined")
	}
	if cand.frequency < refinedFreq-2*fineBinHz || cand.frequency > refinedFreq+2*fineBinHz {
		t.Errorf("refined frequency %.0f Hz, want near %.0f Hz", cand.frequency, refinedFreq)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(activities) != 1 {
		t.Fatalf("activities %d, want 1", len(activities))
	}
	if activities[0].FrequencyHz != cand.frequency {
		t.Errorf("activity frequency %.0f, want refined %.0f", activities[0].FrequencyHz, cand.frequency)
	}
}

// TestWaterfallStatusStrings: a Kiwi without a waterfall says so in
// words, so a spectrum scan is never silently audio-only.
func TestWaterfallStatusStrings(t *testing.T) {
	broken := &kiwiSource{wfErr: errors.New("server busy")}
	if got := broken.WaterfallStatus(); !strings.Contains(got, "unavailable") || !strings.Contains(got, "server busy") {
		t.Errorf("broken waterfall status %q", got)
	}
	live := &kiwiSource{wf: &kiwisdr.WaterfallClient{}}
	if got := live.WaterfallStatus(); got != "waterfall: live panorama" {
		t.Errorf("live waterfall status %q", got)
	}
}
