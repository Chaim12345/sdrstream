package decode

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A cancelled context stops AFSKFramesContext before any burst work and
// returns the context error, so callers can bound packet decode time the
// same way image decoders are bounded.
func TestAFSKFramesContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := AFSKFramesContext(ctx, refAPRS); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled context: got %v, want context.Canceled", err)
	}
}

// The Background wrapper must still decode the off-air reference exactly
// like AFSKFrames always did.
func TestAFSKFramesContextDecodes(t *testing.T) {
	packets, err := AFSKFramesContext(context.Background(), refAPRS)
	if err != nil {
		t.Fatalf("AFSKFramesContext: %v", err)
	}
	if len(packets) == 0 {
		t.Fatal("no packets decoded from the reference APRS audio")
	}
}

// Cancellation must also land mid-decode, not only before the first burst:
// a context cancelled while the ensemble demodulators are running is
// observed at a burst or variant boundary and stops the remaining work.
func TestAFSKFramesContextCancelInFlight(t *testing.T) {
	start := time.Now()
	if _, err := AFSKFramesContext(context.Background(), refAPRS); err != nil {
		t.Fatalf("baseline decode: %v", err)
	}
	base := time.Since(start)
	for _, frac := range []time.Duration{4, 8, 16} {
		ctx, cancel := context.WithCancel(context.Background())
		go func(d time.Duration) {
			time.Sleep(d)
			cancel()
		}(base / frac)
		_, err := AFSKFramesContext(ctx, refAPRS)
		cancel()
		if errors.Is(err, context.Canceled) {
			return
		}
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	t.Fatalf("decode never observed in-flight cancellation (baseline %s)", base)
}

// MorseDecodeContext: a pre-cancelled context stops the decode and the
// Background path still reads the reference.
func TestMorseDecodeContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := MorseDecodeContext(ctx, refMorse); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled context: got %v, want context.Canceled", err)
	}
	res, err := MorseDecodeContext(context.Background(), refMorse)
	if err != nil {
		t.Fatalf("MorseDecodeContext: %v", err)
	}
	if !res.Plausible() {
		t.Fatalf("reference CW read should pass its own plausibility gate, got %+v", res)
	}
}

// Cancellation must also land mid-decode for Morse: cancel while the
// analysis-window loop is running, not only before it starts.
func TestMorseDecodeContextCancelInFlight(t *testing.T) {
	start := time.Now()
	if _, err := MorseDecodeContext(context.Background(), refAPRS); err != nil {
		t.Fatalf("baseline decode: %v", err)
	}
	base := time.Since(start)
	for _, frac := range []time.Duration{4, 8, 16} {
		ctx, cancel := context.WithCancel(context.Background())
		go func(d time.Duration) {
			time.Sleep(d)
			cancel()
		}(base / frac)
		_, err := MorseDecodeContext(ctx, refAPRS)
		cancel()
		if errors.Is(err, context.Canceled) {
			return
		}
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	t.Fatalf("decode never observed in-flight cancellation (baseline %s)", base)
}
