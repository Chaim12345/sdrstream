package decode

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// fakeDecoder writes an executable shell script that ignores its input and
// sleeps, so a cancellation has something slow to interrupt.
func fakeDecoder(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake decoder")
	}
	path := filepath.Join(t.TempDir(), "fake-decoder")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMultimonDecodeHonoursCancellation(t *testing.T) {
	binary := fakeDecoder(t)
	wav := filepath.Join("..", "..", "..", "tests", "data", "ref_aprs.wav")
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled: the job must not run at all
	start := time.Now()
	if _, err := MultimonDecodeWithBinary(ctx, binary, wav, "AFSK1200"); err == nil {
		t.Fatal("cancelled context: expected an error")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("cancelled decode took %v; the fake decoder slept through it", elapsed)
	}
}

func TestDirewolfDecodeHonoursCancellation(t *testing.T) {
	binary := fakeDecoder(t)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := DirewolfDecode(ctx, binary, make([]float64, 8000), 8000); err == nil {
		t.Fatal("cancelled context: expected an error")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("cancelled decode took %v; the fake decoder slept through it", elapsed)
	}
}

func TestExplicitDecoderPathMustExist(t *testing.T) {
	if got := MultimonBinary(filepath.Join(t.TempDir(), "missing")); got != "" {
		t.Errorf("MultimonBinary(missing) = %q, want empty so the caller can reject it", got)
	}
	if got := DirewolfBinary(filepath.Join(t.TempDir(), "missing")); got != "" {
		t.Errorf("DirewolfBinary(missing) = %q, want empty so the caller can reject it", got)
	}
}
