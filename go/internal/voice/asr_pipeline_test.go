package voice

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// scriptedCall is one canned whisper-cli invocation result. These tests run
// the chunked pipeline (VAD -> chunking -> per-chunk runs -> fallback ->
// merge) against a FAKE backend: they pin transcript assembly - chunk count,
// order, fallback choice, language voting - never recognizer accuracy.
type scriptedCall struct {
	text string
	conf float64
	lang string
}

// writeScriptedWhisper installs a fake whisper-cli that answers call n
// (1-based, per invocation counter file) with calls[n-1], honouring -of.
// Calls beyond the script answer with empty text at confidence 0.
func writeScriptedWhisper(t *testing.T, dir string, calls []scriptedCall) string {
	t.Helper()
	counter := filepath.Join(dir, "calls.count")
	var cases strings.Builder
	for i, c := range calls {
		lang := c.lang
		if lang == "" {
			lang = "en"
		}
		var tokens strings.Builder
		for j, w := range strings.Fields(c.text) {
			if j > 0 {
				tokens.WriteByte(',')
			}
			fmt.Fprintf(&tokens, `{"text":%s,"p":%s}`, strconv.Quote(w), strconv.FormatFloat(c.conf, 'f', -1, 64))
		}
		fmt.Fprintf(&cases, "  %d) printf '%%s' '{\"result\":{\"language\":%s},\"transcription\":[{\"text\":%s,\"tokens\":[%s]}]}' > \"$out.json\" ;;\n",
			i+1, strconv.Quote(lang), strconv.Quote(" "+c.text), tokens.String())
	}
	script := fmt.Sprintf(`#!/bin/sh
out=
while [ $# -gt 0 ]; do
  case "$1" in
    -of) out=$2; shift 2 ;;
    *) shift ;;
  esac
done
n=0
[ -f %s ] && n=$(cat %s)
n=$((n + 1))
echo $n > %s
case $n in
%s
  *) printf '%%s' '{"result":{"language":"en"},"transcription":[]}' > "$out.json" ;;
esac
`, strconv.Quote(counter), strconv.Quote(counter), strconv.Quote(counter), cases.String())
	path := filepath.Join(dir, "scripted-whisper")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// twoBursts returns noise | tone | silence | tone | noise at the given rate:
// two speech segments the VAD must keep apart.
func twoBursts(rate int, first, second float64) []float64 {
	rng := rand.New(rand.NewSource(11))
	var s []float64
	s = append(s, noise(rate, 0.8, 0.002, rng)...)
	s = append(s, tone(rate, first, 0.3)...)
	s = append(s, noise(rate, 1.2, 0.002, rng)...)
	s = append(s, tone(rate, second, 0.3)...)
	s = append(s, noise(rate, 0.8, 0.002, rng)...)
	return s
}

func asrIntegrityOptions(binary, dir string, chunkSeconds float64) ASROptions {
	primary := filepath.Join(dir, "primary.bin")
	if err := os.WriteFile(primary, []byte("fake model"), 0o644); err != nil {
		panic(err)
	}
	return ASROptions{
		Options:       Options{Binary: binary, Model: primary},
		MinConfidence: 0.5,
		ChunkSeconds:  chunkSeconds,
	}
}

// Two bursts that cannot share a chunk must come back as one transcript in
// speech order: word-level diffing (WER-style) of the merge is an assembly
// check, not a measurement of any recognizer.
func TestTranscribeASRMergesChunksInSpeechOrder(t *testing.T) {
	dir := t.TempDir()
	binary := writeScriptedWhisper(t, dir, []scriptedCall{
		{text: "alpha bravo", conf: 0.9},
		{text: "charlie delta", conf: 0.9},
	})
	rate := 8000
	res, err := TranscribeASR(context.Background(), twoBursts(rate, 1.2, 1.2), rate, asrIntegrityOptions(binary, dir, 2))
	if err != nil {
		t.Fatalf("TranscribeASR: %v", err)
	}
	if res.Chunks != 2 {
		t.Errorf("chunks = %d, want 2 (one per burst)", res.Chunks)
	}
	if res.Text != "alpha bravo charlie delta" {
		t.Errorf("text = %q, want chunks merged in speech order", res.Text)
	}
	if res.Fallback {
		t.Error("Fallback set although the primary answered every chunk")
	}
}

// A burst longer than ChunkSeconds is split at its quietest frame; the parts
// must still be transcribed in order and reassembled without loss.
func TestTranscribeASRSplitLongBurstKeepsOrder(t *testing.T) {
	dir := t.TempDir()
	binary := writeScriptedWhisper(t, dir, []scriptedCall{
		{text: "w1", conf: 0.9},
		{text: "w2", conf: 0.9},
		{text: "w3", conf: 0.9},
	})
	rate := 8000
	rng := rand.New(rand.NewSource(5))
	var s []float64
	s = append(s, noise(rate, 0.8, 0.002, rng)...)
	s = append(s, tone(rate, 3.2, 0.3)...)
	s = append(s, noise(rate, 0.8, 0.002, rng)...)
	res, err := TranscribeASR(context.Background(), s, rate, asrIntegrityOptions(binary, dir, 1.5))
	if err != nil {
		t.Fatalf("TranscribeASR: %v", err)
	}
	if res.Chunks < 2 {
		t.Fatalf("chunks = %d, want the long burst split into several", res.Chunks)
	}
	if res.Text != "w1 w2 w3" {
		t.Errorf("text = %q, want the split parts in order", res.Text)
	}
}

// The merge must weight each chunk's language vote by its audio length: the
// long English burst beats the short German one.
func TestTranscribeASRLanguageVoteWeightedByChunkLength(t *testing.T) {
	dir := t.TempDir()
	binary := writeScriptedWhisper(t, dir, []scriptedCall{
		{text: "long english report", conf: 0.9, lang: "en"},
		{text: "kurz", conf: 0.9, lang: "de"},
	})
	rate := 8000
	res, err := TranscribeASR(context.Background(), twoBursts(rate, 1.6, 0.5), rate, asrIntegrityOptions(binary, dir, 2))
	if err != nil {
		t.Fatalf("TranscribeASR: %v", err)
	}
	if res.Language != "en" {
		t.Errorf("language = %q, want en (the longer chunk)", res.Language)
	}
	if res.LangProb < 0.6 || res.LangProb > 0.9 {
		t.Errorf("LangProb = %.2f, want the long chunk's share of speech", res.LangProb)
	}
}

// A canned hallucination in one chunk must not swallow the real text of the
// next when no fallback model is configured.
func TestTranscribeASRDropsHallucinatedChunkKeepsRest(t *testing.T) {
	dir := t.TempDir()
	binary := writeScriptedWhisper(t, dir, []scriptedCall{
		{text: "thank you", conf: 0.5}, // canned filler whisper invents on noise
		{text: "mayday relay", conf: 0.9},
	})
	rate := 8000
	res, err := TranscribeASR(context.Background(), twoBursts(rate, 1.2, 1.2), rate, asrIntegrityOptions(binary, dir, 2))
	if err != nil {
		t.Fatalf("TranscribeASR: %v", err)
	}
	if res.Text != "mayday relay" {
		t.Errorf("text = %q, want the clean chunk to survive", res.Text)
	}
}

// Regression: a hallucinated primary used to beat a clean fallback whenever
// its confidence was higher, and the chunk was then dropped as a
// hallucination - real speech vanished. A clean fallback now wins over a
// hallucinated primary even at lower confidence.
func TestTranscribeASRCleanFallbackBeatsHallucinatedPrimary(t *testing.T) {
	dir := t.TempDir()
	// One burst, one chunk: call 1 is the primary inventing "thank you" at
	// 0.7 (a canned hallucination: below the 0.8 canned-phrase bar), call 2
	// is the fallback answering real words at 0.65.
	binary := writeScriptedWhisper(t, dir, []scriptedCall{
		{text: "thank you", conf: 0.7},
		{text: "clear forecast", conf: 0.65},
	})
	primary := filepath.Join(dir, "primary.bin")
	fallback := filepath.Join(dir, "fallback.bin")
	for _, m := range []string{primary, fallback} {
		if err := os.WriteFile(m, []byte("fake model"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rng := rand.New(rand.NewSource(7))
	rate := 8000
	var s []float64
	s = append(s, noise(rate, 0.8, 0.002, rng)...)
	s = append(s, tone(rate, 1.5, 0.3)...)
	s = append(s, noise(rate, 0.8, 0.002, rng)...)
	res, err := TranscribeASR(context.Background(), s, rate, ASROptions{
		Options:       Options{Binary: binary, Model: primary},
		FallbackModel: fallback,
		MinConfidence: 0.5,
		ChunkSeconds:  30,
	})
	if err != nil {
		t.Fatalf("TranscribeASR: %v", err)
	}
	if res.Text != "clear forecast" {
		t.Errorf("text = %q, want the clean fallback transcript", res.Text)
	}
	if !res.Fallback {
		t.Error("Fallback flag not set after the fallback model won")
	}
	if res.Model != "fallback.bin" {
		t.Errorf("model = %q, want fallback.bin", res.Model)
	}
}

// Marker-only output is whisper labelling non-speech, not a transcript: it
// must be dropped at any confidence, and a clean fallback that clears
// MinConfidence must get its chance against it.
func TestTranscribeASRDropsMarkerOnlyChunk(t *testing.T) {
	dir := t.TempDir()
	binary := writeScriptedWhisper(t, dir, []scriptedCall{{text: "[music]", conf: 0.95}})
	rate := 8000
	res, err := TranscribeASR(context.Background(), twoBursts(rate, 1.2, 1.2), rate, asrIntegrityOptions(binary, dir, 30))
	if err != nil {
		t.Fatalf("TranscribeASR: %v", err)
	}
	if res.Text != "" {
		t.Errorf("text = %q, want marker-only output dropped", res.Text)
	}
}

func TestTranscribeASRMarkerPrimaryYieldsToCleanFallback(t *testing.T) {
	dir := t.TempDir()
	binary := writeScriptedWhisper(t, dir, []scriptedCall{
		{text: "[music]", conf: 0.95},
		{text: "clear forecast", conf: 0.85},
	})
	o := asrIntegrityOptions(binary, dir, 30)
	fallback := filepath.Join(dir, "fallback.bin")
	if err := os.WriteFile(fallback, []byte("fake model"), 0o644); err != nil {
		t.Fatal(err)
	}
	o.FallbackModel = fallback
	rate := 8000
	res, err := TranscribeASR(context.Background(), twoBursts(rate, 1.2, 1.2), rate, o)
	if err != nil {
		t.Fatalf("TranscribeASR: %v", err)
	}
	if res.Text != "clear forecast" {
		t.Errorf("text = %q, want the clean fallback over the marker primary", res.Text)
	}
	if !res.Fallback {
		t.Error("Fallback flag not set")
	}
}

// QA pin: a clean-but-unsure fallback (below MinConfidence, the same bar
// under which the primary itself is retried) must NOT rescue a chunk from a
// hallucinated primary - retried-grade output is dropped, not published.
func TestTranscribeASRLowConfidenceFallbackDoesNotBeatHallucination(t *testing.T) {
	dir := t.TempDir()
	binary := writeScriptedWhisper(t, dir, []scriptedCall{
		{text: "thank you", conf: 0.7},
		{text: "mumbled static words here", conf: 0.3},
	})
	o := asrIntegrityOptions(binary, dir, 30) // MinConfidence 0.5
	fallback := filepath.Join(dir, "fallback.bin")
	if err := os.WriteFile(fallback, []byte("fake model"), 0o644); err != nil {
		t.Fatal(err)
	}
	o.FallbackModel = fallback
	rate := 8000
	res, err := TranscribeASR(context.Background(), twoBursts(rate, 1.2, 1.2), rate, o)
	if err != nil {
		t.Fatalf("TranscribeASR: %v", err)
	}
	if res.Text != "" {
		t.Errorf("text = %q, want the below-bar fallback dropped with the chunk", res.Text)
	}
	if res.Fallback {
		t.Error("Fallback flag set although the fallback never won")
	}
}

// PlanChunks is exported: hand-built segments outside the audio must be
// clamped, not sliced into a panic.
func TestPlanChunksClampsOutOfRangeSegments(t *testing.T) {
	rate := 8000
	x := make([]float64, rate) // 1 s
	chunks := PlanChunks(x, rate, []SpeechSegment{{0, 0.5}, {2.0, 3.0}, {-1, 0.2}}, 25)
	if len(chunks) != 1 {
		t.Fatalf("chunks = %d, want 1", len(chunks))
	}
	// splitLong drops the fully out-of-range segment {2,3} and clamps the
	// negative start: 0.5 s of audio + 150 ms join silence + 0.2 s.
	want := rate/2 + rate*3/20 + rate/5
	if len(chunks[0].Samples) != want {
		t.Errorf("chunk samples = %d, want %d after clamping", len(chunks[0].Samples), want)
	}
}

// splitLong must clamp segments to the audio: no cut and no returned segment
// may reach past the last sample.
func TestSplitLongClampsToAudio(t *testing.T) {
	rate := 8000
	x := make([]float64, rate) // 1 s
	segs := splitLong(x, rate, []SpeechSegment{{0.5, 3.0}, {2.0, 2.5}, {0.1, 0.4}}, 0.3)
	// {0.5,3} clamps to {0.5,1} and splits into three; {2,2.5} is wholly
	// outside and dropped; {0.1,0.4} splits into two.
	if len(segs) != 5 {
		t.Fatalf("segments = %d, want 5 (out-of-range one skipped): %+v", len(segs), segs)
	}
	for _, s := range segs {
		if s.End > 1.0 || s.Start < 0 || s.End <= s.Start {
			t.Errorf("segment %+v escapes the audio", s)
		}
	}
}
