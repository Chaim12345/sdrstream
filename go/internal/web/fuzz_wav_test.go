package web

import (
	"os"
	"path/filepath"
	"testing"
)

// R5 fuzz: the spectrogram's WAV reader is a second, independent RIFF
// parser; torn captures must not panic or hang it either.
// Run: go test -fuzz=FuzzReadWAVSamples -fuzztime=60s ./internal/web/
func FuzzReadWAVSamples(f *testing.F) {
	dir := filepath.Join("..", "..", "..", "tests", "data")
	for _, name := range []string{"ref_pocsag.wav", "ref_dtmf.wav"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			f.Fatalf("missing committed fixture %s: %v", name, err)
		}
		if len(b) > 48000 {
			b = b[:48000]
		}
		f.Add(b)
		f.Add(b[:44])
		f.Add(b[:12])
		f.Add(b[:20])
	}
	f.Add([]byte("not a wav at all"))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip("cap fuzz input at 1 MiB")
		}
		p := filepath.Join(t.TempDir(), "fuzz.wav")
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		samples, rate, err := readWAVSamples(p)
		if err != nil {
			return
		}
		// Whatever the reader accepted feeds the spectrogram FFT path:
		// render must not panic on hostile rate/length combinations.
		_, _ = spectrogramPNG(samples, rate)
	})
}
