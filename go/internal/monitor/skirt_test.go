package monitor

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"
)

func TestSuppressSkirtPeaks(t *testing.T) {
	fm := peak{frequency: 100.800e6, bandwidth: 55_000, levelDB: 27.5}
	edge1 := peak{frequency: 100.7634e6, bandwidth: 0, levelDB: 10.5}
	edge2 := peak{frequency: 100.8415e6, bandwidth: 2_000, levelDB: 10.1}
	// Real-world shape from the 2026-10-25 S23 device logs: two wide FM
	// stations plus a dozen 0-4 kHz edge hits ~10 dB over the floor.
	got := suppressSkirtPeaks([]peak{fm, edge1, edge2})
	if len(got) != 1 || got[0].frequency != fm.frequency {
		t.Fatalf("edge hits survived: %+v", got)
	}
}

func TestSuppressSkirtPeaksKeepsIndependentSignals(t *testing.T) {
	fm := peak{frequency: 100.800e6, bandwidth: 55_000, levelDB: 27.5}
	cases := []struct {
		name string
		p    peak
	}{
		{"narrow far away from the wide signal", peak{frequency: 101.900e6, bandwidth: 3_000, levelDB: 11}},
		{"narrow near a wide but WEAKER signal", peak{frequency: 95.0e6, bandwidth: 2_000, levelDB: 30}},
		{"unknown-width (0) far from wide signals", peak{frequency: 144.8e6, bandwidth: 0, levelDB: 12}},
	}
	for _, tc := range cases {
		got := suppressSkirtPeaks([]peak{fm, tc.p})
		if len(got) != 2 {
			t.Fatalf("%s: wrongly suppressed: %+v", tc.name, got)
		}
	}
}

func TestSuppressSkirtPeaksNeverSuppressesViaNarrowSignals(t *testing.T) {
	// Two narrow signals near each other: neither is wide enough to
	// suppress the other (an SSB pileup must not eat its neighbours).
	a := peak{frequency: 14.074e6, bandwidth: 3_000, levelDB: 25}
	b := peak{frequency: 14.076e6, bandwidth: 2_000, levelDB: 10}
	got := suppressSkirtPeaks([]peak{a, b})
	if len(got) != 2 {
		t.Fatalf("narrow suppressor ate a neighbour: %+v", got)
	}
}

func TestSuppressSkirtPeaksKeepsWideSignalItself(t *testing.T) {
	fm := peak{frequency: 100.800e6, bandwidth: 55_000, levelDB: 27.5}
	edge := peak{frequency: 100.76e6, bandwidth: 1_000, levelDB: 10}
	got := suppressSkirtPeaks([]peak{fm, edge})
	found := false
	for _, p := range got {
		if p.frequency == fm.frequency {
			found = true
		}
	}
	if !found {
		t.Fatalf("the wide signal itself was suppressed: %+v", got)
	}
}

// fineSource is a 1024-bin fake with a 3 MHz span (~2.93 kHz/bin), fine
// enough for a 1-bin peak to count as narrow (<=5 kHz) and a 20-bin run
// to count as wide (>=20 kHz). Mirrors panoSource's interface.
type fineSource struct {
	frames [][]float64
	idx    int
}

func (f *fineSource) Spectrum() ([]float64, float64, float64, bool) {
	frame := f.frames[f.idx%len(f.frames)]
	f.idx++
	return frame, 15e6, 3e6, true
}

func (f *fineSource) RefineSpectrum(ctx context.Context, freqHz float64) ([]float64, float64, float64, bool) {
	return nil, 0, 0, false
}

func (f *fineSource) Listen(ctx context.Context, hz float64, mode string, dur time.Duration) ([]float64, int, error) {
	return nil, 0, errors.New("not implemented")
}
func (f *fineSource) Close() error { return nil }

// TestStepSuppressesSkirtPeaks wires suppression into step(): a strong
// wide signal with narrow edge spikes one step later leaves exactly one
// active candidate - the wide signal. Without the step() call-out the
// spikes each create their own candidate (the device-log failure mode).
func TestStepSuppressesSkirtPeaks(t *testing.T) {
	frame := make([]float64, 1024)
	for i := range frame {
		frame[i] = -105
	}
	for i := 600; i <= 619; i++ { // 20 bins ~ 58.6 kHz wide, strong
		frame[i] = -65
	}
	frame[595] = -80 // narrow skirt spike, 15 dB below the wide signal
	frame[625] = -80
	m := New(Options{
		Source:      &fineSource{frames: [][]float64{frame}},
		ThresholdDB: 10, AverageFrames: 1, DwellTime: time.Millisecond,
	})
	m.step(context.Background())
	m.mu.Lock()
	got := len(m.active)
	var survivor float64
	for _, c := range m.active {
		survivor = c.frequency
	}
	m.mu.Unlock()
	if got != 1 {
		t.Fatalf("skirt spikes created %d candidates through step(), want 1", got)
	}
	// Survivor sits at the wide signal: bin ~609.5 -> +278 kHz above centre.
	if math.Abs(survivor-15.278e6) > 50e3 {
		t.Errorf("surviving candidate at %.0f Hz, want near the wide signal (~15.278 MHz)", survivor)
	}
}
