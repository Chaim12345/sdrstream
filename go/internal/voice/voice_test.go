package voice

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
