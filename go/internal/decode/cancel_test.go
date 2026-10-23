package decode

import (
	"context"
	"errors"
	"testing"
	"time"
)

func readFixtureWAV(t *testing.T, path string) ([]float64, int) {
	t.Helper()
	samples, rate, err := ReadWAV(path)
	if err != nil {
		t.Skip("fixture not present:", err)
	}
	return samples, rate
}

// In-flight cancellation: the context is cancelled AFTER the decode has
// started (5 ms into a decode that takes far longer), so the checks
// inside the sample loops are what stop the work - not a pre-cancelled
// early return. Asserts prompt return and the ctx error, no result.
func TestAPTDecodeContextCancelInFlight(t *testing.T) {
	// ~750 s of synthetic APT at 11025 Hz: envelope + NCC take
	// hundreds of milliseconds, far past the 5 ms cancel.
	samples := synthAPT(1500, 11025, 0.05)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(5*time.Millisecond, cancel)
	start := time.Now()
	r, err := APTDecodeContext(ctx, samples, 11025)
	el := time.Since(start)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled (result %v)", err, r != nil)
	}
	if r != nil {
		t.Fatal("cancelled decode returned a result")
	}
	if el > 5*time.Second {
		t.Fatalf("cancelled decode took %v; loops did not stop promptly", el)
	}
}

func TestSSTVImageContextCancelInFlight(t *testing.T) {
	wav, rate := readFixtureWAV(t, "testdata/sstv_robot36.wav")
	// Tile the fixture so detection/demod work takes far past 5 ms.
	big := make([]float64, 0, len(wav)*8)
	for i := 0; i < 8; i++ {
		big = append(big, wav...)
	}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(5*time.Millisecond, cancel)
	start := time.Now()
	png, err := SSTVImageContext(ctx, big, rate)
	el := time.Since(start)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if png != nil {
		t.Fatal("cancelled decode returned a PNG")
	}
	if el > 5*time.Second {
		t.Fatalf("cancelled decode took %v; loops did not stop promptly", el)
	}
}

// API preservation: the existing non-context entry points still decode.
func TestUncancelledWrappersStillWork(t *testing.T) {
	r := APTDecode(synthAPT(150, 11025, 0.05), 11025)
	if ok, why := r.Verified(); !ok {
		t.Fatalf("synthetic APT not verified via wrapper: %s", why)
	}
	wav, wrate := readFixtureWAV(t, "testdata/sstv_robot36.wav")
	if _, err := SSTVImage(wav, wrate); err != nil {
		t.Fatalf("SSTVImage wrapper: %v", err)
	}
}
