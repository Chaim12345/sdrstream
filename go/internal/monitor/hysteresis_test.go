package monitor

import (
	"math"
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

// fineWindow builds a zoomed-window frame at floor with peaks at the
// given bin offsets; garbage bins are NaN (saturated artifacts).
func fineWindow(floor float64, peaks map[int]float64, naBins ...int) []float64 {
	bins := make([]float64, 1024)
	for i := range bins {
		bins[i] = floor
	}
	for bin, level := range peaks {
		bins[bin] = floor + level
	}
	for _, bin := range naBins {
		bins[bin] = math.NaN()
	}
	return bins
}

func refineInstance(source *panoSource) *Monitor {
	m := New(Options{Source: source, ThresholdDB: 10, DwellTime: time.Millisecond})
	m.mu.Lock()
	m.lastBinHz = 30e6 / 1024 // coarse Kiwi panorama bin
	m.mu.Unlock()
	return m
}

// TestRefineIgnoresStrongerNeighbour: a peak outside the coarse run's
// span is a different signal; refinement must not jump to it, even when
// it is much stronger than the peak that is actually ours.
func TestRefineIgnoresStrongerNeighbour(t *testing.T) {
	const coarseBinHz = 30e6 / 1024
	const fineBinHz = coarseBinHz / 32
	const coarseFreq = 7.2e6
	// Our signal: 20 dB, 5 fine bins below the coarse estimate (well
	// inside the +-1 coarse bin identity span). Neighbour: 60 dB, two
	// coarse bins away.
	ourBin := 512 - 5
	neighbourBin := 512 + 64
	span := 1024 * fineBinHz
	centre := coarseFreq - (float64(ourBin)+0.5)*fineBinHz + span/2
	source := &panoSource{
		refineBins:   fineWindow(-100, map[int]float64{ourBin: 20, neighbourBin: 60}),
		refineCentre: centre,
		refineSpan:   span,
	}
	cand := &candidate{frequency: coarseFreq, bandwidth: coarseBinHz, levelDB: 18}
	refineInstance(source).refine(context.Background(), cand)
	if !cand.refined {
		t.Fatal("same-span peak was not accepted")
	}
	ourFreq := centre - span/2 + (float64(ourBin)+0.5)*fineBinHz
	if math.Abs(cand.frequency-ourFreq) > 2*fineBinHz {
		t.Errorf("refined to %.0f Hz, want our peak near %.0f Hz", cand.frequency, ourFreq)
	}
	neighbourFreq := centre - span/2 + (float64(neighbourBin)+0.5)*fineBinHz
	if math.Abs(cand.frequency-neighbourFreq) < coarseBinHz/2 {
		t.Errorf("refined jumped to the stronger neighbour at %.0f Hz", neighbourFreq)
	}
}

// TestRefineKeepsCoarseWhenOnlyNeighbours: no fine peak inside the
// identity span means the coarse estimate stands; refining must not
// invent a frequency from a neighbouring signal.
func TestRefineKeepsCoarseWhenOnlyNeighbours(t *testing.T) {
	const coarseBinHz = 30e6 / 1024
	const fineBinHz = coarseBinHz / 32
	const coarseFreq = 14.1e6
	span := 1024 * fineBinHz
	// Only a strong neighbour three coarse bins away.
	source := &panoSource{
		refineBins:   fineWindow(-100, map[int]float64{512 + 96: 50}),
		refineCentre: coarseFreq,
		refineSpan:   span,
	}
	cand := &candidate{frequency: coarseFreq, bandwidth: coarseBinHz, levelDB: 18}
	refineInstance(source).refine(context.Background(), cand)
	if cand.refined {
		t.Errorf("refined onto a neighbour at %.0f Hz; coarse %.0f should have stood",
			cand.frequency, coarseFreq)
	}
	if cand.frequency != coarseFreq {
		t.Errorf("frequency moved to %.0f without a same-signal peak", cand.frequency)
	}
}

// TestDetectExcludesSaturatedBins: NaN bins (saturated server garbage,
// e.g. the DC region of a low zoomed window) never form peaks and never
// poison the noise floor of the real signal next to them.
func TestDetectExcludesSaturatedBins(t *testing.T) {
	na := make([]int, 0, 40)
	for i := 0; i < 40; i++ {
		na = append(na, i)
	}
	bins := fineWindow(-105, map[int]float64{300: 25}, na...)
	m := New(Options{ThresholdDB: 10})
	peaks := m.detectGuarded(bins, 468750, 937500, false)
	if len(peaks) != 1 {
		t.Fatalf("peaks %v, want exactly the real one", peaks)
	}
	if peaks[0].frequency > 500e3 {
		t.Errorf("peak at %.0f Hz, want the real one near %.0f Hz", peaks[0].frequency, (300.5)*937500/1024)
	}
	if peaks[0].levelDB > 40 {
		t.Errorf("levelDB %.1f inflated by a poisoned floor", peaks[0].levelDB)
	}
}

// TestDetectAllSaturatedWindow: a window that is entirely saturated
// garbage reports nothing at all.
func TestDetectAllSaturatedWindow(t *testing.T) {
	bins := make([]float64, 1024)
	for i := range bins {
		bins[i] = math.NaN()
	}
	m := New(Options{ThresholdDB: 10})
	if peaks := m.detectGuarded(bins, 468750, 937500, false); len(peaks) != 0 {
		t.Fatalf("all-saturated window produced %d peaks", len(peaks))
	}
}

// TestReportSkipsListenForUnrefinedCoarse: a coarse candidate whose
// refinement finds no same-signal peak is published explicitly marked
// with its frequency uncertainty, and no narrowband recording job is
// dispatched for its bin-centre frequency.
func TestReportSkipsListenForUnrefinedCoarse(t *testing.T) {
	ctx := context.Background()
	pano := panoFrame(-105, 30, 246)
	// Zoomed window carries only a strong neighbour two coarse bins
	// away: refinement must not match, so the candidate stays unrefined.
	const coarseBinHz = 30e6 / 1024
	const fineBinHz = coarseBinHz / 32
	coarseFreq := (246 + 0.5) * coarseBinHz
	span := 1024 * fineBinHz
	source := &panoSource{
		frames:       [][]float64{pano},
		refineBins:   fineWindow(-100, map[int]float64{512 + 64: 50}),
		refineCentre: coarseFreq,
		refineSpan:   span,
	}
	var activities []Activity
	var mu sync.Mutex
	instance := New(Options{
		Source: source, ThresholdDB: 10, DwellTime: time.Millisecond, Listen: 5 * time.Second,
		OnActivity: func(a Activity) { mu.Lock(); activities = append(activities, a); mu.Unlock() },
	})
	instance.step(ctx)
	time.Sleep(2 * time.Millisecond)
	instance.step(ctx)
	mu.Lock()
	defer mu.Unlock()
	if len(activities) != 1 {
		t.Fatalf("activities %d, want 1 (the marked unrefined candidate)", len(activities))
	}
	a := activities[0]
	if a.FrequencyRefined {
		t.Error("unmatched candidate marked refined")
	}
	if a.FrequencyUncertaintyHz != coarseBinHz/2 {
		t.Errorf("uncertainty %.0f Hz, want %.0f (half a coarse bin)", a.FrequencyUncertaintyHz, coarseBinHz/2)
	}
	if len(instance.jobs) != 0 {
		t.Errorf("narrowband listen dispatched for an unrefined candidate (%d jobs)", len(instance.jobs))
	}
}

// TestReportDispatchesListenAfterRefine: once refinement confirms the
// same signal, the recording job carries the refined frequency and the
// activity is marked refined with no uncertainty.
func TestReportDispatchesListenAfterRefine(t *testing.T) {
	ctx := context.Background()
	pano := panoFrame(-105, 30, 246)
	const coarseBinHz = 30e6 / 1024
	const fineBinHz = coarseBinHz / 32
	coarseFreq := (246 + 0.5) * coarseBinHz
	span := 1024 * fineBinHz
	source := &panoSource{
		frames:       [][]float64{pano},
		refineBins:   fineWindow(-100, map[int]float64{508: 30}),
		refineCentre: coarseFreq,
		refineSpan:   span,
	}
	var activities []Activity
	var mu sync.Mutex
	instance := New(Options{
		Source: source, ThresholdDB: 10, DwellTime: time.Millisecond, Listen: 5 * time.Second,
		OnActivity: func(a Activity) { mu.Lock(); activities = append(activities, a); mu.Unlock() },
	})
	instance.step(ctx)
	time.Sleep(2 * time.Millisecond)
	instance.step(ctx)
	mu.Lock()
	defer mu.Unlock()
	if len(instance.jobs) != 1 {
		t.Fatalf("jobs %d, want 1 recording for the refined candidate", len(instance.jobs))
	}
	work := <-instance.jobs
	wantFreq := coarseFreq - span/2 + (508+0.5)*fineBinHz
	if work.frequency < wantFreq-2*fineBinHz || work.frequency > wantFreq+2*fineBinHz {
		t.Errorf("job frequency %.0f, want refined %.0f", work.frequency, wantFreq)
	}
	var recorded *Activity
	for i := range instance.activities {
		if instance.activities[i].ID == work.activityID {
			recorded = &instance.activities[i]
		}
	}
	if recorded == nil {
		t.Fatal("no stored activity for the job")
	}
	if !recorded.FrequencyRefined || recorded.FrequencyUncertaintyHz != 0 {
		t.Errorf("refined activity markers wrong: refined=%v uncertainty=%.0f",
			recorded.FrequencyRefined, recorded.FrequencyUncertaintyHz)
	}
}
