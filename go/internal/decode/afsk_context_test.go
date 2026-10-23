package decode

import (
	"context"
	"errors"
	"testing"
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
