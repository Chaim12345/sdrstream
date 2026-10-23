package decode

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// R5 fuzz: torn and mutated WAVs must never panic or hang the decoders.
// A capture torn on disk (interrupted recording, truncated upload) used to
// be a real crash class for this pipeline, so the readers and the decoder
// entry points the gallery drives are fuzzed end to end. Errors are fine;
// panics, OOM-scale allocations and hangs are findings.
//
// Run: go test -fuzz=FuzzReadWAV -fuzztime=90s ./internal/decode/

func fuzzSeeds(f *testing.F) {
	f.Helper()
	fuzzSeedsCapped(f, 48000)
}

// fuzzSeedsCapped seeds the corpus with committed fixtures truncated at
// seedCap; the full-decode target uses a smaller cap so the seeds stay
// cheap when plain `go test` replays them as regression cases.
func fuzzSeedsCapped(f *testing.F, seedCap int) {
	f.Helper()
	dir := filepath.Join("..", "..", "..", "tests", "data")
	for _, name := range []string{"ref_pocsag.wav", "ref_eas.wav", "ref_aprs.wav", "ref_dtmf.wav"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			f.Fatalf("missing committed fixture %s: %v", name, err)
		}
		if len(b) > seedCap {
			b = b[:seedCap] // keep seeds fast; structure lives in the header
		}
		f.Add(b)
		// Torn at structural boundaries: header only, RIFF header only,
		// mid-fmt-chunk, one byte into data.
		f.Add(b[:44])
		f.Add(b[:12])
		f.Add(b[:20])
		f.Add(b[:45])
	}
	f.Add([]byte("not a wav at all"))
	f.Add([]byte{})
}

func fuzzWrite(t *testing.T, data []byte) string {
	t.Helper()
	if len(data) > 1<<20 {
		t.Skip("cap fuzz input at 1 MiB so a run cannot OOM on samples alone")
	}
	p := filepath.Join(t.TempDir(), "fuzz.wav")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func FuzzReadWAV(f *testing.F) {
	fuzzSeeds(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		p := fuzzWrite(t, data)
		samples, rate, err := ReadWAV(p)
		if err != nil {
			return
		}
		// Whatever the reader accepted must be safe for the pure-Go
		// demodulators: no panics, no unbounded work from a hostile
		// rate/channels header.
		_ = AFSK1200(samples, rate)
		_ = squelchPower(samples, rate)
		_ = POCSAGDecode(samples, rate, 1200)
		_ = NAVTEXDecode(samples, rate, 0)
		_ = DTMFDecode(samples, rate)
	})
}

func FuzzDecodeTornWAV(f *testing.F) {
	// Minimal seeds: plain `go test` replays them, and each costs a full
	// AFSK + Morse pass. Deep coverage comes from actual -fuzz runs.
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "tests", "data", "ref_aprs.wav"))
	if err != nil {
		f.Fatalf("missing committed fixture: %v", err)
	}
	f.Add(b[:4096])
	f.Add(b[:44])
	f.Add([]byte("not a wav at all"))
	f.Fuzz(func(t *testing.T, data []byte) {
		p := fuzzWrite(t, data)
		// The full path the gallery drives: file to frames/text. A short
		// ctx keeps a pathological input from stalling the fuzz worker;
		// deadline errors are fine, panics are not.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = AFSKFramesContext(ctx, p)
		_, _ = MorseDecodeContext(ctx, p)
	})
}
