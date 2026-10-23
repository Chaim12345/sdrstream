package monitor

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestLiveKiwiPanoramaDetect runs the real detection loop over a live
// public KiwiSDR waterfall panorama when KIWI_LIVE=<host> is set. It is
// the end-to-end proof that a Kiwi now feeds the spectrum monitor:
// connect, collect waterfall lines, detect peaks, print what is on the
// air. Skipped in offline runs.
func TestLiveKiwiPanoramaDetect(t *testing.T) {
	host := os.Getenv("KIWI_LIVE")
	if host == "" {
		t.Skip("set KIWI_LIVE=<host> to run against a public KiwiSDR")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	source, err := KiwiSource(ctx, host)
	if err != nil {
		t.Skipf("no server: %v", err)
	}
	defer source.Close()
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
	instance := New(Options{ThresholdDB: 10})
	peaks := instance.detect(bins, centre, span)
	t.Logf("panorama %.1f-%.1f MHz, %d peaks:", (centre-span/2)/1e6, (centre+span/2)/1e6, len(peaks))
	for _, p := range peaks {
		t.Logf("  %9.4f MHz  width>=%.0f kHz  %.1f dB above floor", p.frequency/1e6, p.bandwidth/1e3, p.levelDB)
	}
	if len(peaks) == 0 {
		t.Error("no activity detected on a live HF panorama (unlikely)")
	}
}
