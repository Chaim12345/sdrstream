package kiwisdr

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestLiveWaterfall pulls a few waterfall lines from a real public
// KiwiSDR when KIWI_LIVE=<host> is set; skipped otherwise, because
// network calls never belong in an offline test run.
func TestLiveWaterfall(t *testing.T) {
	host := os.Getenv("KIWI_LIVE")
	if host == "" {
		t.Skip("set KIWI_LIVE=<host> to run against a public KiwiSDR")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	wf, err := NewWaterfall(host, DefaultOptions())
	if err != nil {
		t.Skipf("no server: %v", err)
	}
	if err := wf.Connect(ctx); err != nil {
		t.Skipf("connect failed: %v", err)
	}
	defer wf.Close()
	var bins []float64
	var centre, span float64
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		b, c, s, ok := wf.Spectrum()
		if ok {
			bins, centre, span = b, c, s
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if len(bins) == 0 {
		t.Fatal("no waterfall frame arrived")
	}
	t.Logf("spectrum: %d bins, centre %.3f MHz, span %.3f MHz", len(bins), centre/1e6, span/1e6)
	if centre <= 0 || span <= 0 {
		t.Fatalf("absurd geometry: centre %v span %v", centre, span)
	}
	lo, hi := bins[0], bins[0]
	for _, b := range bins {
		if b < lo {
			lo = b
		}
		if b > hi {
			hi = b
		}
	}
	t.Logf("levels: min %.1f dB max %.1f dB", lo, hi)
	if hi-lo < 10 {
		t.Errorf("flat spectrum: range %.1f dB across %d bins", hi-lo, len(bins))
	}
}
