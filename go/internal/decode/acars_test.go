package decode

import (
	"math/rand"
	"os"
	"testing"
)

// TestACARSRealRecording decodes the acarsdec project's real 4-channel
// test.wav (LGPL, not vendored here). Set SDRSTREAM_ACARS_WAV to it. The
// reference acarsdec output lists 7 blocks; the CRC+parity gate must
// reproduce them.
func TestACARSRealRecording(t *testing.T) {
	p := os.Getenv("SDRSTREAM_ACARS_WAV")
	if p == "" {
		t.Skip("SDRSTREAM_ACARS_WAV not set")
	}
	chs, rate, err := ReadWAVChannels(p)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for c, s := range chs {
		for _, m := range ACARSDecode(s, rate, c) {
			total++
			t.Logf("ch%d %.2fs %s", c, m.AtSec, m)
		}
	}
	if total < 7 {
		t.Fatalf("decoded %d blocks, acarsdec reference has 7", total)
	}
}

func TestACARSNoiseYieldsNothing(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	s := make([]float64, 12500*20)
	for i := range s {
		s[i] = rng.NormFloat64()
	}
	if m := ACARSDecode(s, 12500, 0); len(m) != 0 {
		t.Fatalf("noise decoded %d blocks", len(m))
	}
}
