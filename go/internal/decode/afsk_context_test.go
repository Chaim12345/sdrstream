package decode

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
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
//
// The reference APRS audio decodes in milliseconds, so a wall-clock cancel
// fraction of it races the scheduler: a cold baseline read inflates the
// measured decode time and every later warm decode finishes before its
// cancel fires, which made this test fail deterministically under load.
// Tiling the reference into a long multi-burst WAV keeps the cancel point
// orders of magnitude beyond scheduling jitter, so the observation is
// deterministic instead of lucky.
func TestAFSKFramesContextCancelInFlight(t *testing.T) {
	samples, rate, err := ReadWAV(refAPRS)
	if err != nil {
		t.Fatalf("read reference: %v", err)
	}
	gap := make([]float64, rate/2)
	var long []float64
	for i := 0; i < 60; i++ {
		long = append(long, samples...)
		long = append(long, gap...)
	}
	path := writeTestWAV(t, long, rate)

	// A counting context makes the in-flight observation deterministic:
	// the decode checks ctx.Err() per burst and per ensemble variant, so
	// failing after half the checks proves the remaining checks - and the
	// demodulation work behind them - never ran. No wall clock is
	// involved, so nothing here can race the scheduler.
	var total int64
	full := &countingErrCtx{Context: context.Background(), calls: &total, failAfter: -1}
	if _, err := AFSKFramesContext(full, path); err != nil {
		t.Fatalf("baseline decode: %v", err)
	}
	if total < 10 {
		t.Fatalf("expected many cancellation checkpoints over the tiled file, got %d", total)
	}
	var calls int64
	half := &countingErrCtx{Context: context.Background(), calls: &calls, failAfter: total / 2}
	_, err = AFSKFramesContext(half, path)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("half-way cancellation: got %v, want context.Canceled", err)
	}
	if got := atomic.LoadInt64(&calls); got != total/2+1 {
		t.Fatalf("decode stopped after %d ctx checks, want exactly %d (total %d)", got, total/2+1, total)
	}
}

// countingErrCtx reports context.Canceled once Err has been consulted
// failAfter times, so a test can cancel at an exact burst or variant
// boundary instead of a wall-clock guess. failAfter < 0 never cancels.
type countingErrCtx struct {
	context.Context
	calls     *int64
	failAfter int64
}

func (c *countingErrCtx) Err() error {
	n := atomic.AddInt64(c.calls, 1)
	if c.failAfter >= 0 && n > c.failAfter {
		return context.Canceled
	}
	return c.Context.Err()
}

// writeTestWAV encodes mono float samples as a 16-bit PCM WAV in a temp
// dir, the exact subset of the format ReadWAV parses.
func writeTestWAV(t *testing.T, samples []float64, rate int) string {
	t.Helper()
	data := make([]byte, 44+2*len(samples))
	copy(data[0:4], "RIFF")
	binary.LittleEndian.PutUint32(data[4:8], uint32(len(data)-8))
	copy(data[8:12], "WAVE")
	copy(data[12:16], "fmt ")
	binary.LittleEndian.PutUint32(data[16:20], 16)
	binary.LittleEndian.PutUint16(data[20:22], 1) // PCM
	binary.LittleEndian.PutUint16(data[22:24], 1) // mono
	binary.LittleEndian.PutUint32(data[24:28], uint32(rate))
	binary.LittleEndian.PutUint32(data[28:32], uint32(rate*2))
	binary.LittleEndian.PutUint16(data[32:34], 2)
	binary.LittleEndian.PutUint16(data[34:36], 16)
	copy(data[36:40], "data")
	binary.LittleEndian.PutUint32(data[40:44], uint32(2*len(samples)))
	for i, v := range samples {
		if v > 1 {
			v = 1
		}
		if v < -1 {
			v = -1
		}
		binary.LittleEndian.PutUint16(data[44+i*2:], uint16(int16(v*32767)))
	}
	path := filepath.Join(t.TempDir(), "long.wav")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write temp wav: %v", err)
	}
	return path
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
