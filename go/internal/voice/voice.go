// Package voice turns radio audio into text.
//
// Transcription runs through whisper.cpp, which is a small C++ program with
// a GGML model file: the binary is invoked as an external tool, the same
// way the multimon-ng and direwolf decoders are used, so no cgo or model
// format handling is needed inside the Go tree. The model and the binary
// are looked up in ~/.cache/sdrstream/whisper by default, and both can be
// overridden with SDRSTREAM_WHISPER and SDRSTREAM_WHISPER_MODEL.
//
// When no speech backend is installed the package still reports speech
// activity (energy based voice activity detection), so a receiver can say
// "voice transmission here" even without a transcript.
package voice

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Transcript is the result of a transcription run.
type Transcript struct {
	Text    string  `json:"text"`
	Backend string  `json:"backend"`
	Seconds float64 `json:"seconds"`
	Words   []Word  `json:"words,omitempty"`
	Spoken  bool    `json:"spoken"`
}

// Word is one recognised word with its timing.
type Word struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Text  string  `json:"text"`
}

// Options configures the speech backend.
type Options struct {
	// Binary is the whisper.cpp command; empty means "look it up".
	Binary string
	// Model is the GGML model file; empty means "look it up".
	Model string
	// Language is the language hint passed to whisper ("auto" detects).
	Language string
	// Threads limits the inference threads; 0 lets whisper decide.
	Threads int
}

// CacheDir is where the whisper binary and models are kept.
var CacheDir = func() string {
	if dir := os.Getenv("SDRSTREAM_WHISPER_DIR"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".sdrstream-whisper"
	}
	return filepath.Join(home, ".cache", "sdrstream", "whisper")
}()

// DefaultOptions returns the options from the environment.
func DefaultOptions() Options {
	return Options{
		Binary:   os.Getenv("SDRSTREAM_WHISPER"),
		Model:    os.Getenv("SDRSTREAM_WHISPER_MODEL"),
		Language: envOr("SDRSTREAM_WHISPER_LANG", "auto"),
		Threads:  0,
	}
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

// FindBinary locates the whisper.cpp command.
func FindBinary(options Options) string {
	if options.Binary != "" {
		if path, err := exec.LookPath(options.Binary); err == nil {
			return path
		}
		if _, err := os.Stat(options.Binary); err == nil {
			return options.Binary
		}
		return ""
	}
	if path, err := exec.LookPath("whisper-cli"); err == nil {
		return path
	}
	for _, candidate := range []string{
		filepath.Join(CacheDir, "whisper-cli"),
		filepath.Join(CacheDir, "whisper.cpp", "whisper-cli"),
	} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

// FindModel locates a GGML model file.
func FindModel(options Options) string {
	if options.Model != "" {
		if _, err := os.Stat(options.Model); err == nil {
			return options.Model
		}
		return ""
	}
	matches, _ := filepath.Glob(filepath.Join(CacheDir, "ggml-*.bin"))
	if len(matches) == 0 {
		matches, _ = filepath.Glob(filepath.Join(CacheDir, "*.bin"))
	}
	// Prefer the English "en" models, then the smallest (fastest) one.
	sortByPreference(matches)
	if len(matches) > 0 {
		return matches[0]
	}
	return ""
}

// sortByPreference puts the small English models first.
func sortByPreference(paths []string) {
	score := func(path string) int {
		name := filepath.Base(path)
		switch {
		case strings.Contains(name, "tiny.en"):
			return 0
		case strings.Contains(name, "base.en"):
			return 1
		case strings.Contains(name, "tiny"):
			return 2
		case strings.Contains(name, "base"):
			return 3
		case strings.Contains(name, "small"):
			return 4
		}
		return 5
	}
	for i := 1; i < len(paths); i++ {
		for j := i; j > 0 && score(paths[j]) < score(paths[j-1]); j-- {
			paths[j], paths[j-1] = paths[j-1], paths[j]
		}
	}
}

// Available reports whether a speech backend can be used.
func Available(options Options) bool {
	return FindBinary(options) != "" && FindModel(options) != ""
}

// Transcribe recognises speech in the samples.
func Transcribe(ctx context.Context, samples []float64, rate int, options Options) (Transcript, error) {
	transcript := Transcript{Seconds: float64(len(samples)) / float64(rate)}
	if len(samples) < rate/10 {
		return transcript, fmt.Errorf("voice: too short to transcribe")
	}
	binary := FindBinary(options)
	model := FindModel(options)
	if binary == "" {
		return transcript, fmt.Errorf("voice: no whisper.cpp binary found (set SDRSTREAM_WHISPER)")
	}
	if model == "" {
		return transcript, fmt.Errorf("voice: no GGML model found (set SDRSTREAM_WHISPER_MODEL)")
	}

	// whisper.cpp wants 16 kHz mono.
	pcm := Resample(samples, rate, 16000)
	file, err := os.CreateTemp("", "sdrstream-voice-*.wav")
	if err != nil {
		return transcript, err
	}
	defer os.Remove(file.Name())
	if err := WriteWAV(file.Name(), pcm, 16000); err != nil {
		file.Close()
		return transcript, err
	}
	file.Close()

	args := []string{"-m", model, "-f", file.Name(), "-nt", "-np"}
	if options.Language != "" && options.Language != "auto" {
		args = append(args, "-l", options.Language)
	}
	if options.Threads > 0 {
		args = append(args, "-t", strconv.Itoa(options.Threads))
	}
	runCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	command := exec.CommandContext(runCtx, binary, args...)
	// whisper.cpp writes the transcript to stdout and its progress log to
	// stderr, so they are kept apart.
	var stdout, stderr strings.Builder
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return transcript, fmt.Errorf("voice: %v: %s", err, tail(stderr.String(), 200))
	}
	text := cleanTranscript(stdout.String())
	transcript.Text = text
	transcript.Backend = "whisper.cpp"
	transcript.Spoken = text != ""
	return transcript, nil
}

// isWhisperLog reports whether a line is whisper.cpp's own output rather
// than part of the transcript.
func isWhisperLog(line string) bool {
	for _, prefix := range []string{
		"whisper_", "main:", "system_info:", "register_backend", "load_backend",
		"ggml_", "error:", "read_audio_data:", "init_", "graph_", "print_",
		"model_", "params", "backend", "encoder", "decoder", "alloc",
	} {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

// tail returns the last n characters of a string.
func tail(text string, n int) string {
	if len(text) <= n {
		return text
	}
	return text[len(text)-n:]
}

// cleanTranscript strips whisper.cpp's console noise and keeps the text.
func cleanTranscript(raw string) string {
	lines := strings.Split(raw, "\n")
	var kept []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// Skip whisper.cpp's own status lines: they are written to the
		// standard output as well, mixed into the transcript.
		if isWhisperLog(trimmed) {
			continue
		}
		if strings.Contains(trimmed, " --> ") { // timestamped segment
			if i := strings.Index(trimmed, "] "); i >= 0 {
				trimmed = trimmed[i+2:]
			}
		}
		kept = append(kept, trimmed)
	}
	return strings.TrimSpace(strings.Join(kept, " "))
}

// Resample converts samples to a new rate with linear interpolation.
func Resample(samples []float64, from, to int) []float64 {
	if from == to || len(samples) == 0 || to <= 0 || from <= 0 {
		return samples
	}
	if len(samples) == 1 {
		return append([]float64(nil), samples...)
	}
	ratio := float64(from) / float64(to)
	out := make([]float64, int(float64(len(samples))/ratio))
	last := len(samples) - 2
	for i := range out {
		position := float64(i) * ratio
		i0 := int(position)
		if i0 > last {
			i0 = last
		}
		if i0 < 0 {
			i0 = 0
		}
		frac := position - float64(i0)
		if frac < 0 {
			frac = 0
		}
		if frac > 1 {
			frac = 1
		}
		out[i] = samples[i0]*(1-frac) + samples[i0+1]*frac
	}
	return out
}

// WriteWAV writes 16-bit mono PCM to a file.
func WriteWAV(path string, samples []float64, rate int) error {
	data := make([]byte, 2*len(samples))
	for i, s := range samples {
		value := int16(math.Round(s * 32767))
		binary.LittleEndian.PutUint16(data[2*i:], uint16(value))
	}
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	header := make([]byte, 44)
	copy(header[0:], "RIFF")
	binary.LittleEndian.PutUint32(header[4:], uint32(36+len(data)))
	copy(header[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(header[16:], 16)
	binary.LittleEndian.PutUint16(header[20:], 1) // PCM
	binary.LittleEndian.PutUint16(header[22:], 1) // mono
	binary.LittleEndian.PutUint32(header[24:], uint32(rate))
	binary.LittleEndian.PutUint32(header[28:], uint32(rate*2))
	binary.LittleEndian.PutUint16(header[32:], 2)  // block align
	binary.LittleEndian.PutUint16(header[34:], 16) // bits
	copy(header[36:], "data")
	binary.LittleEndian.PutUint32(header[40:], uint32(len(data)))
	if _, err := file.Write(header); err != nil {
		return err
	}
	_, err = file.Write(data)
	return err
}

// Envelope returns how strongly a recording is modulated: the coefficient
// of variation and the crest factor of the short-term level.
//
// AGC keeps the level of every capture roughly the same, so the absolute
// level says nothing about whether a signal is there. The envelope does:
// speech and other modulated signals swing wildly between syllables,
// while a carrier or a steady noise floor does not.
func Envelope(samples []float64, rate int) (cv, crest float64) {
	frame := rate / 50 // 20 ms
	if frame < 1 {
		frame = 1
	}
	if len(samples) < frame*4 {
		return 0, 1
	}
	var rms []float64
	for start := 0; start+frame <= len(samples); start += frame {
		sum := 0.0
		for _, s := range samples[start : start+frame] {
			sum += s * s
		}
		rms = append(rms, math.Sqrt(sum/float64(frame)))
	}
	if len(rms) < 4 {
		return 0, 1
	}
	mean := 0.0
	for _, v := range rms {
		mean += v
	}
	mean /= float64(len(rms))
	if mean <= 0 {
		return 0, 1
	}
	variance := 0.0
	peak := 0.0
	for _, v := range rms {
		difference := v - mean
		variance += difference * difference
		if v > peak {
			peak = v
		}
	}
	variance /= float64(len(rms))
	return math.Sqrt(variance) / mean, peak / mean
}

// Modulated reports whether a recording carries a modulated signal rather
// than a steady carrier or a noise floor.
func Modulated(samples []float64, rate int) bool {
	cv, crest := Envelope(samples, rate)
	return cv >= 0.2 || crest >= 1.6
}

// SpeechSegment is a stretch of audio that looks like speech.
type SpeechSegment struct {
	Start float64
	End   float64
}

// SpeechActivity finds the parts of a recording that carry speech, using
// energy against a noise floor. It is used when no transcription backend
// is installed, and as a cheap pre-check before transcribing.
func SpeechActivity(samples []float64, rate int) []SpeechSegment {
	if len(samples) < rate/4 {
		return nil
	}
	frame := rate / 50 // 20 ms
	if frame < 1 {
		frame = 1
	}
	var rms []float64
	for start := 0; start+frame <= len(samples); start += frame {
		sum := 0.0
		for _, s := range samples[start : start+frame] {
			sum += s * s
		}
		rms = append(rms, math.Sqrt(sum/float64(frame)))
	}
	if len(rms) == 0 {
		return nil
	}
	sorted := append([]float64(nil), rms...)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j] < sorted[j-1]; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	floor := sorted[len(sorted)/4]
	threshold := floor * 3
	if threshold < 0.01 {
		threshold = 0.01
	}
	var segments []SpeechSegment
	inSpeech := false
	start := 0
	for i, value := range rms {
		if value >= threshold && !inSpeech {
			inSpeech = true
			start = i
		} else if value < threshold && inSpeech {
			inSpeech = false
			segments = append(segments, SpeechSegment{
				Start: float64(start) * 0.02,
				End:   float64(i) * 0.02,
			})
		}
	}
	if inSpeech {
		segments = append(segments, SpeechSegment{
			Start: float64(start) * 0.02,
			End:   float64(len(rms)) * 0.02,
		})
	}
	// Drop blips shorter than 100 ms.
	var kept []SpeechSegment
	for _, segment := range segments {
		if segment.End-segment.Start >= 0.1 {
			kept = append(kept, segment)
		}
	}
	return kept
}
