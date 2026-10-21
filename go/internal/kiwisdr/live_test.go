package kiwisdr

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

// TestLiveAudio records a few seconds from a real public KiwiSDR when
// KIWI_LIVE=<host> is set; it is skipped otherwise because network calls
// never belong in an offline test run.
func TestLiveAudio(t *testing.T) {
	host := os.Getenv("KIWI_LIVE")
	if host == "" {
		t.Skip("set KIWI_LIVE=<host> to run against a public KiwiSDR")
	}
	freqkHz := 14074.0
	if v := os.Getenv("KIWI_LIVE_FREQ_KHZ"); v != "" {
		if _, err := fmt.Sscanf(v, "%f", &freqkHz); err != nil {
			t.Fatalf("bad KIWI_LIVE_FREQ_KHZ: %v", err)
		}
	}
	ctx := context.Background()
	client, err := New(host, DefaultOptions())
	if err != nil {
		t.Skipf("no server: %v", err)
	}
	if err := client.Connect(ctx); err != nil {
		t.Skipf("connect failed: %v", err)
	}
	defer client.Close()
	if err := client.Tune(ctx, int64(freqkHz*1000), "usb"); err != nil {
		t.Fatalf("tune: %v", err)
	}
	samples, rate, err := client.Record(ctx, 6*time.Second)
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if len(samples) == 0 {
		t.Fatal("no audio")
	}
	if rate < 8000 || rate > 66000 {
		t.Errorf("absurd rate %d", rate)
	}
	peak := 0
	for _, sample := range samples {
		if sample < 0 {
			sample = -sample
		}
		if int(sample) > peak {
			peak = int(sample)
		}
	}
	t.Logf("%d samples rate %d peak %d info %+v", len(samples), rate, peak, client.InfoSnapshot())
	if peak == 0 {
		t.Log("audio is silent; the server may be quiet at this frequency")
	}
}
