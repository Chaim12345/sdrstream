package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/decode"
	"github.com/chaim12345/sdrstream/go/internal/web"
)

// fakeSpectrumSource is an OpenWebRX-shaped receiver: it always shows one
// wide peak in a 2 MHz profile and replays one real recording when the
// monitor tunes it.
type fakeSpectrumSource struct {
	samples []float64
	rate    int

	mu      sync.Mutex
	listens int
}

func (f *fakeSpectrumSource) Spectrum() ([]float64, float64, float64, bool) {
	bins := make([]float64, 2048)
	for i := 500; i < 530; i++ {
		bins[i] = 30
	}
	return bins, 119e6, 2e6, true
}

func (f *fakeSpectrumSource) Listen(ctx context.Context, _ float64, _ string, _ time.Duration) ([]float64, int, error) {
	f.mu.Lock()
	f.listens++
	f.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil, 0, ctx.Err()
	default:
		return f.samples, f.rate, nil
	}
}

func (f *fakeSpectrumSource) Close() error { return nil }

// TestScanOpenWebRXSpectrumPublishesVerifiedDecode drives the survey path
// with a fake receiver: the real APRS recording must reach the gallery as a
// verified artifact, and the session row must end as "session complete"
// rather than falling back to the fixed target list.
func TestScanOpenWebRXSpectrumPublishesVerifiedDecode(t *testing.T) {
	t.Setenv("SDRSTREAM_STATE_DIR", t.TempDir())
	samples, rate, err := decode.ReadWAV("../../../tests/data/ref_aprs.wav")
	if err != nil {
		t.Fatalf("read APRS recording: %v", err)
	}
	source := &fakeSpectrumSource{samples: samples, rate: rate}
	server := web.New(t.TempDir(), t.TempDir())

	scansMu.Lock()
	scans = nil
	scansMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	started := time.Now()
	scanOpenWebRXSpectrum(ctx, server, source, "OpenWebRX", "fake.receiver", 20, "", "", t.TempDir())
	if elapsed := time.Since(started); elapsed > 20*time.Second {
		t.Fatalf("survey took %v, want the 6 s session bound", elapsed)
	}

	scansMu.Lock()
	defer scansMu.Unlock()
	var spectrumDone, decoded bool
	for _, item := range scans {
		switch {
		case item.Mode == "spectrum" && item.State == "session complete":
			spectrumDone = true
		case item.Mode != "spectrum" && item.State == "decoded":
			decoded = true
		}
	}
	if !spectrumDone {
		t.Errorf("spectrum session did not finish cleanly: %+v", scans)
	}
	if !decoded {
		t.Errorf("the APRS recording was not published: %+v", scans)
	}
	if source.listens == 0 {
		t.Error("the monitor never listened to the detected peak")
	}
}
