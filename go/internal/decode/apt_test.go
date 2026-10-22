package decode

import (
	"math"
	"math/rand"
	"os"
	"testing"
)

// Synthetic APT: only a structural sanity check of the demod and gate. It
// is NOT evidence that real recordings decode; see TestAPTRealRecording.
func synthAPT(lines int, rate int, noise float64) []float64 {
	rng := rand.New(rand.NewSource(1))
	words := make([]float64, 0, lines*aptLineLen)
	tA := aptTemplate(4, 7, 4)
	tB := aptTemplate(5, 7, 4)
	for l := 0; l < lines; l++ {
		line := make([]float64, aptLineLen)
		for x := range line {
			line[x] = 0.5 + 0.3*math.Sin(float64(x+l)/40)
		}
		copy(line, tA)
		copy(line[1040:], tB)
		words = append(words, line...)
	}
	n := len(words) * rate / aptWordRate
	out := make([]float64, n)
	for i := range out {
		w := words[int(float64(i)*aptWordRate/float64(rate))]
		out[i] = (0.2+0.8*w)*math.Sin(2*math.Pi*2400*float64(i)/float64(rate))*0.5 + noise*rng.NormFloat64()
	}
	return out
}

func TestAPTSyntheticAndNoise(t *testing.T) {
	r := APTDecode(synthAPT(150, 11025, 0.05), 11025)
	if ok, why := r.Verified(); !ok {
		t.Fatalf("synthetic APT not verified: %s (%+v)", why, r)
	}
	rng := rand.New(rand.NewSource(7))
	noise := make([]float64, 11025*80)
	for i := range noise {
		noise[i] = rng.NormFloat64() * 0.3
	}
	if r := APTDecode(noise, 11025); r != nil {
		if ok, _ := r.Verified(); ok {
			t.Fatal("white noise passed the APT gate")
		}
	}
}

// TestAPTRealRecording runs on a real NOAA APT recording when
// SDRSTREAM_APT_WAV points at one (e.g. noaa-apt's argentina.wav).
func TestAPTRealRecording(t *testing.T) {
	p := os.Getenv("SDRSTREAM_APT_WAV")
	if p == "" {
		t.Skip("SDRSTREAM_APT_WAV not set")
	}
	s, rate, err := ReadWAV(p)
	if err != nil {
		t.Fatal(err)
	}
	r := APTDecode(s, rate)
	ok, why := r.Verified()
	t.Logf("lines=%d locked=%d syncA=%.2f syncB=%.2f verified=%v (%s)", r.Lines, r.LinesLocked, r.MeanSyncA, r.SyncB, ok, why)
	if !ok {
		t.Fatal(why)
	}
	if d := os.Getenv("SDRSTREAM_APT_OUT"); d != "" {
		b, _ := r.PNG()
		os.WriteFile(d, b, 0o644)
	}
}
