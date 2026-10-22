package voice

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/chaim12345/sdrstream/go/internal/decode"
)

func TestWriteWAVRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.wav")
	samples := []float64{0, 0.5, -0.5, 1, -1}
	if err := WriteWAV(path, samples, 12000); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		t.Fatalf("not a RIFF/WAVE file: %q", data[0:12])
	}
	rate := int(data[24]) | int(data[25])<<8 | int(data[26])<<16 | int(data[27])<<24
	if rate != 12000 {
		t.Errorf("rate = %d, want 12000", rate)
	}
	body := data[44:]
	if len(body) != 2*len(samples) {
		t.Fatalf("body is %d bytes, want %d", len(body), 2*len(samples))
	}
	for i, want := range samples {
		got := int16(uint16(body[2*i]) | uint16(body[2*i+1])<<8)
		if math.Abs(float64(got)/32768-want) > 0.001 {
			t.Errorf("sample %d = %v, want %v", i, float64(got)/32768, want)
		}
	}
}

func TestResample(t *testing.T) {
	rate := 8000
	samples := make([]float64, rate)
	for i := range samples {
		samples[i] = math.Sin(2 * math.Pi * 1000 * float64(i) / float64(rate))
	}
	out := Resample(samples, rate, 16000)
	if len(out) != 2*len(samples) {
		t.Errorf("length = %d, want %d", len(out), 2*len(samples))
	}
	// The tone must still be at 1 kHz: count the zero crossings.
	crossings := 0
	for i := 1; i < len(out); i++ {
		if (out[i-1] < 0) != (out[i] < 0) {
			crossings++
		}
	}
	expected := 2000 // 1 kHz over one second
	if crossings < expected-100 || crossings > expected+100 {
		t.Errorf("zero crossings = %d, want about %d: the tone moved", crossings, expected)
	}
}

func TestResampleDoesNotPanic(t *testing.T) {
	_ = Resample([]float64{0.5}, 8000, 16000)
	_ = Resample(nil, 8000, 16000)
}

func TestEnvelope(t *testing.T) {
	rate := 8000
	// White noise: steady, not modulated.
	noise := make([]float64, rate)
	seed := uint32(7)
	for i := range noise {
		seed = seed*1664525 + 1013904223
		noise[i] = float64(int32(seed)) / float64(1<<31)
	}
	if Modulated(noise, rate) {
		cv, crest := Envelope(noise, rate)
		t.Errorf("white noise looks modulated (cv=%.2f crest=%.2f)", cv, crest)
	}
	// An amplitude modulated tone: modulated.
	am := make([]float64, rate)
	for i := range am {
		envelope := 0.2 + 0.8*math.Abs(math.Sin(2*math.Pi*5*float64(i)/float64(rate)))
		am[i] = envelope * math.Sin(2*math.Pi*800*float64(i)/float64(rate))
	}
	if !Modulated(am, rate) {
		cv, crest := Envelope(am, rate)
		t.Errorf("an amplitude modulated tone looks unmodulated (cv=%.2f crest=%.2f)", cv, crest)
	}
}

func TestSpeechActivity(t *testing.T) {
	rate := 8000
	samples := make([]float64, rate)
	// A burst of speech-like noise in the middle of silence.
	for i := rate / 2; i < rate/2+rate/4; i++ {
		seed := uint32(uint32(i) * 2654435761)
		seed = seed*1664525 + 1013904223
		samples[i] = float64(int32(seed)) / float64(1<<31) * 0.5
	}
	segments := SpeechActivity(samples, rate)
	if len(segments) == 0 {
		t.Fatal("no speech segment found in a burst")
	}
	if segments[0].Start < 0.4 || segments[0].Start > 0.6 {
		t.Errorf("segment starts at %.2f s, want about 0.5 s", segments[0].Start)
	}
}

func TestCleanTranscript(t *testing.T) {
	raw := "read_audio_data: reading audio data from '/tmp/x.wav' ...\n" +
		"read_audio_data: trying to decode with miniaudio\n" +
		"\n He hoped there would be stew for dinner.\n"
	text := cleanTranscript(raw)
	if strings.Contains(text, "read_audio_data") {
		t.Errorf("the transcript still contains the log lines: %q", text)
	}
	if !strings.Contains(text, "stew for dinner") {
		t.Errorf("text = %q, want the sentence", text)
	}
}

func TestFindBinaryAndModel(t *testing.T) {
	options := DefaultOptions()
	if FindBinary(options) != "" && FindModel(options) == "" {
		t.Error("a binary was found but no model")
	}
}

// TestWhisperArgs checks the command line built for whisper.cpp. The beam
// size is the interesting one: greedy decoding (the upstream default) drops
// words on narrowband radio audio, so anything above 1 must be passed on.
func TestWhisperArgs(t *testing.T) {
	args := whisperArgs("/models/ggml-large-v3.bin", "/tmp/clip.wav", Options{
		BeamSize: 5, Threads: 4, Language: "en",
	})
	for _, want := range []string{"-m", "/models/ggml-large-v3.bin", "-f", "/tmp/clip.wav",
		"-bs", "5", "-t", "4", "-l", "en", "-nt", "-np"} {
		if !contains(args, want) {
			t.Errorf("args %v do not contain %q", args, want)
		}
	}

	plain := whisperArgs("m", "w", Options{})
	for _, unwanted := range []string{"-bs", "-t", "-l", "--prompt"} {
		if contains(plain, unwanted) {
			t.Errorf("default args %v must not contain %q", plain, unwanted)
		}
	}

	hinted := whisperArgs("m", "w", Options{Prompt: "aviation radio phraseology"})
	if !containsString(hinted, "aviation radio phraseology") {
		t.Errorf("args %v dropped the prompt", hinted)
	}
}

// TestWhisperBeamDefaultsToFive checks the environment default: an unset
// SDRSTREAM_WHISPER_BEAM means a beam of five, not whisper.cpp's greedy 1.
func TestWhisperBeamDefaultsToFive(t *testing.T) {
	if got := DefaultOptions().BeamSize; got != 5 {
		t.Errorf("default beam size = %d, want 5", got)
	}
	value, found := os.LookupEnv("SDRSTREAM_WHISPER_BEAM")
	if found {
		if got := DefaultOptions().BeamSize; got != atoiOr(value) {
			t.Errorf("beam size = %d, want the value from the environment (%s)", got, value)
		}
	}
}

// TestLargeModelIsPreferred checks that a large GGML model actually installed
// in the cache is found: FindModel prefers the small English models for
// speed, but a large model must never be missed.
func TestLargeModelIsPreferred(t *testing.T) {
	options := DefaultOptions()
	if options.Model != "" {
		t.Skip("SDRSTREAM_WHISPER_MODEL pins the model")
	}
	matches, _ := filepath.Glob(filepath.Join(CacheDir, "ggml-*.bin"))
	if len(matches) == 0 {
		t.Skip("no model installed")
	}
	model := FindModel(options)
	if model == "" {
		t.Fatal("models are installed in the cache but none was found")
	}
	if !strings.Contains(model, "large") {
		t.Logf("note: the model in use is %s, not a large one", filepath.Base(model))
	}
}

// TestTranscribeRealRadioClip takes the first recording the PoC monitor made
// and transcribes it for real: this is the end-to-end proof that a
// transmission found on the air can be turned into text. It needs a
// whisper.cpp binary and a model, and a recording to work on, so it skips
// when either is missing (set SDRSTREAM_WHISPER / SDRSTREAM_WHISPER_MODEL
// and WHISPER_LIVE_CLIP to run it).
func TestTranscribeRealRadioClip(t *testing.T) {
	if FindBinary(DefaultOptions()) == "" || FindModel(DefaultOptions()) == "" {
		t.Skip("no whisper.cpp binary or model installed")
	}
	candidates := []string{}
	if clip := os.Getenv("WHISPER_LIVE_CLIP"); clip != "" {
		candidates = []string{clip}
	} else {
		candidates, _ = filepath.Glob("../../../captures/poc/*.wav")
		if len(candidates) == 0 {
			candidates, _ = filepath.Glob("../../../captures/monitor/*.wav")
		}
	}
	if len(candidates) == 0 {
		t.Skip("no real recording to transcribe (run the monitor PoC first)")
	}
	samples, rate, err := decode.ReadWAV(candidates[0])
	if err != nil {
		t.Fatalf("read %s: %v", candidates[0], err)
	}
	options := DefaultOptions()
	transcript, err := Transcribe(context.Background(), samples, rate, options)
	if err != nil {
		t.Fatalf("transcribe %s: %v", candidates[0], err)
	}
	t.Logf("%s (%.1fs, %d samples, %d Hz, beam %d, %s) -> %q",
		filepath.Base(candidates[0]), transcript.Seconds, len(samples), rate,
		options.BeamSize, filepath.Base(FindModel(options)), transcript.Text)
	if transcript.Backend != "whisper.cpp" {
		t.Errorf("backend = %q, want whisper.cpp", transcript.Backend)
	}
	if !transcript.Spoken {
		t.Log("no text in this clip: the recording may hold no speech")
	}
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if strings.Contains(item, want) {
			return true
		}
	}
	return false
}

func atoiOr(value string) int {
	number, err := strconv.Atoi(value)
	if err != nil {
		return 0
	}
	return number
}

// TestWriteWAVAtomic: the write is tmp+rename, leaving a complete WAV and
// no ".tmp" residue for a scanner to trip over.
func TestWriteWAVAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clip.wav")
	if err := WriteWAV(path, []float64{0.5, -0.5, 0}, 8000); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 44+6 || string(data[:4]) != "RIFF" {
		t.Fatalf("unexpected WAV bytes: %d %q", len(data), data[:4])
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temp file left behind: %v", err)
	}
}
