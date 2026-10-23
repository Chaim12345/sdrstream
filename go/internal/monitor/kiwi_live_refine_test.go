package monitor

import (
	"context"
	"math"
	"os"
	"testing"
	"time"
)

// TestLiveKiwiDetectRefineRecord is the end-to-end live proof of the
// targeted workflow on a public KiwiSDR when KIWI_LIVE=<host> is set:
// panorama detect, zoom refine to ~1.5 kHz bins, then a short targeted
// recording of the refined candidate frequency. Peaks are reported as
// candidates only; no station or mode identity is claimed. Skipped in
// offline runs.
func TestLiveKiwiDetectRefineRecord(t *testing.T) {
	host := os.Getenv("KIWI_LIVE")
	if host == "" {
		t.Skip("set KIWI_LIVE=<host> to run against a public KiwiSDR")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	source, err := KiwiSource(ctx, host)
	if err != nil {
		t.Skipf("no server: %v", err)
	}
	defer source.Close()
	ks, ok := source.(*kiwiSource)
	if !ok {
		t.Fatalf("source is %T, want *kiwiSource", source)
	}
	t.Logf("status: %s", ks.WaterfallStatus())

	var bins []float64
	var centre, span float64
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		b, c, s, ok := source.Spectrum()
		if ok {
			bins, centre, span = b, c, s
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if len(bins) == 0 {
		t.Fatal("kiwi source served no spectrum")
	}

	instance := New(Options{Source: source, ThresholdDB: 10})
	peaks := instance.detect(bins, centre, span)
	if len(peaks) == 0 {
		t.Skip("no activity on the panorama right now; nothing to refine")
	}
	// detect returns peaks in bin order; refine and record the
	// strongest. A marginal peak can fade under the zoomed window's
	// local floor, and that is RF, not a code failure - so below 15 dB
	// the run skips instead of flaking.
	best := peaks[0]
	for _, p := range peaks[1:] {
		if p.levelDB > best.levelDB {
			best = p
		}
	}
	if best.levelDB < 15 {
		t.Skipf("strongest candidate only %.1f dB above floor; nothing solid to refine", best.levelDB)
	}
	t.Logf("coarse candidate: %.4f MHz, %.1f dB above floor (candidate, not a confirmed station)",
		best.frequency/1e6, best.levelDB)

	cand := &candidate{frequency: best.frequency, levelDB: best.levelDB, bandwidth: best.bandwidth}
	// step() normally records the coarse bin width before refine runs;
	// this test drives refine directly, so seed it the same way.
	instance.mu.Lock()
	instance.lastBinHz = span / float64(len(bins))
	instance.mu.Unlock()
	instance.refine(ctx, cand)
	if !cand.refined {
		t.Fatal("refine produced no zoomed spectrum")
	}
	// refine accepts a fine peak up to one coarse bin from the coarse
	// estimate (bin-centre uncertainty plus edge leakage), so that is
	// the honest bound on the correction.
	coarseBin := span / float64(len(bins))
	if math.Abs(cand.frequency-best.frequency) > coarseBin {
		t.Fatalf("refined frequency jumped %.0f Hz, more than one coarse bin (%.0f Hz)",
			math.Abs(cand.frequency-best.frequency), coarseBin)
	}
	t.Logf("refined candidate: %.4f MHz, %.1f dB above floor (candidate, not a confirmed station)",
		cand.frequency/1e6, cand.levelDB)

	samples, rate, err := source.Listen(ctx, cand.frequency, "usb", 6*time.Second)
	if err != nil {
		t.Fatalf("targeted listen: %v", err)
	}
	if len(samples) == 0 {
		t.Fatal("targeted listen returned no audio")
	}
	var sum float64
	for _, s := range samples {
		sum += math.Abs(s)
	}
	mean := sum / float64(len(samples))
	t.Logf("recorded %.1fs at %d Hz, mean |sample| %.4f", float64(len(samples))/float64(rate), rate, mean)
	if mean < 1e-4 {
		t.Error("targeted recording is silent; detection did not lead to usable audio")
	}
}
