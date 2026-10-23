package voice

import (
	"math"
	"math/rand"
	"testing"
)

func tone(rate int, sec, amp float64) []float64 {
	out := make([]float64, int(sec*float64(rate)))
	for i := range out {
		out[i] = amp * math.Sin(2*math.Pi*500*float64(i)/float64(rate)) * (0.6 + 0.4*math.Sin(2*math.Pi*4*float64(i)/float64(rate)))
	}
	return out
}

func noise(rate int, sec, sigma float64, rng *rand.Rand) []float64 {
	out := make([]float64, int(sec*float64(rate)))
	for i := range out {
		out[i] = rng.NormFloat64() * sigma
	}
	return out
}

// SYNTHETIC audio: checks the mechanics of the detector, not speech quality.
func TestDetectSpeechBridgesPausesAndPads(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	rate := 8000
	var x []float64
	x = append(x, noise(rate, 2, 0.002, rng)...)
	x = append(x, tone(rate, 1, 0.3)...)
	x = append(x, noise(rate, 0.2, 0.002, rng)...) // pause shorter than the hangover
	x = append(x, tone(rate, 1, 0.3)...)
	x = append(x, noise(rate, 2, 0.002, rng)...)
	segs := DetectSpeech(x, rate, VADOptions{})
	if len(segs) != 1 {
		t.Fatalf("want 1 merged segment, got %v", segs)
	}
	if segs[0].Start > 2.0 || segs[0].Start < 1.7 || segs[0].End < 4.2 || segs[0].End > 4.5 {
		t.Fatalf("segment bounds off: %v", segs[0])
	}
}

func TestDetectSpeechSilenceIsEmpty(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	if segs := DetectSpeech(noise(8000, 5, 0.002, rng), 8000, VADOptions{}); len(segs) != 0 {
		t.Fatalf("noise detected as speech: %v", segs)
	}
}

func TestPlanChunksSplitsAtPauses(t *testing.T) {
	rate := 8000
	x := make([]float64, 60*rate)
	segs := []SpeechSegment{{0, 10}, {12, 22}, {30, 40}, {45, 50}}
	chunks := PlanChunks(x, rate, segs, 25)
	if len(chunks) != 2 {
		t.Fatalf("want 2 chunks, got %d", len(chunks))
	}
	if chunks[0].Start != 0 || chunks[0].End != 22 || chunks[1].Start != 30 {
		t.Fatalf("bad chunk bounds: %+v", chunks)
	}
}

func TestPlanChunksCutsLongSegment(t *testing.T) {
	rate := 8000
	x := make([]float64, 70*rate)
	for i := range x {
		x[i] = 0.3
	}
	chunks := PlanChunks(x, rate, []SpeechSegment{{0, 60}}, 25)
	if len(chunks) < 3 {
		t.Fatalf("60 s segment should need 3+ chunks of <=25 s, got %d", len(chunks))
	}
	for _, c := range chunks {
		if c.End-c.Start > 25.01 {
			t.Fatalf("chunk too long: %+v", c)
		}
	}
}

func TestParseWhisperJSON(t *testing.T) {
	js := `{"result":{"language":"de"},"transcription":[{"text":" Hallo Welt","tokens":[
	{"text":"[_BEG_]","p":0.99},{"text":" Hallo","p":0.9},{"text":" Welt","p":0.5},{"text":"[_TT_50]","p":0.1}]}]}`
	text, lang, conf, err := ParseWhisperJSON([]byte(js))
	if err != nil || text != "Hallo Welt" || lang != "de" || math.Abs(conf-0.7) > 1e-9 {
		t.Fatalf("got %q %q %v %v", text, lang, conf, err)
	}
}

func TestHallucination(t *testing.T) {
	if !hallucination("Thank you.", 0.5) || hallucination("Thank you.", 0.95) {
		t.Fatal("short filler should be dropped only at low confidence")
	}
	if !hallucination("go go go go go go go go", 0.9) {
		t.Fatal("repetition loop not caught")
	}
	if hallucination("november two three alpha bravo over", 0.6) {
		t.Fatal("real traffic dropped")
	}
	// Marker-only output and the guard's canned YouTube phrases fail at any
	// confidence: they are never broadcast content (guard.go contract).
	for _, text := range []string{"[music]", "(applause)", "[BLANK_AUDIO]", "Thank you for watching", "subtitles by the community"} {
		if !hallucination(text, 0.95) {
			t.Fatalf("%q is a hallucination at any confidence", text)
		}
	}
	if hallucination("thanks for watching", 0.95) != true {
		t.Fatal("canned phrase fails at any confidence after the guard alignment")
	}
}
