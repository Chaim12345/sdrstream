package monitor

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The "ubersdr" kind must route to the UberSDR dialer, not fall through to
// "unknown receiver kind". Against a dead loopback port the dial itself
// fails fast; the assertion is on the error's provenance, and the same call
// with a nonsense kind proves the guard still rejects unmapped kinds.
func TestSourceForUberSDRKindRoutesToDialer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := sourceFor(ctx, "ubersdr", "127.0.0.1:1", "", true)
	if err == nil {
		t.Fatal("dead loopback port unexpectedly dialled")
	}
	if strings.Contains(err.Error(), "unknown receiver kind") {
		t.Fatalf("ubersdr kind not routed: %v", err)
	}
}

func TestSourceForWeb888KindRoutesToDialer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := sourceFor(ctx, "web-888", "127.0.0.1:1", "", true)
	if err == nil {
		t.Fatal("dead loopback port unexpectedly dialled")
	}
	if strings.Contains(err.Error(), "unknown receiver kind") {
		t.Fatalf("web-888 kind not routed: %v", err)
	}
}

func TestSourceForUnknownKindStillRejected(t *testing.T) {
	ctx := context.Background()
	for _, kind := range []string{"nosuchkind", "uber", "web888"} {
		_, err := sourceFor(ctx, kind, "127.0.0.1:1", "", true)
		if err == nil || !strings.Contains(err.Error(), "unknown receiver kind") {
			t.Fatalf("kind %q: want unknown-kind error, got %v", kind, err)
		}
	}
}
