package voice

// Speech recognition quality layer: voice activity detection with
// hysteresis, chunking at silences, language identification, a confidence
// score from whisper's own token probabilities, and a fallback to a second
// local model when the first one is unsure.

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// VADOptions tunes DetectSpeech. The zero value means "use the defaults".
type VADOptions struct {
	Frame      time.Duration // analysis frame, default 20 ms
	StartRatio float64       // energy over the floor that opens a segment, default 3.0
	EndRatio   float64       // energy over the floor that keeps one open, default 2.0
	Hangover   time.Duration // silence tolerated inside a segment, default 400 ms
	MinSpeech  time.Duration // shorter segments are dropped, default 200 ms
	Pad        time.Duration // context kept on both sides of a segment, default 150 ms
	MinRMS     float64       // absolute floor for the threshold, default 0.005
}

func (o VADOptions) withDefaults() VADOptions {
	if o.Frame <= 0 {
		o.Frame = 20 * time.Millisecond
	}
	if o.StartRatio <= 0 {
		o.StartRatio = 3.0
	}
	if o.EndRatio <= 0 {
		o.EndRatio = 2.0
	}
	if o.Hangover <= 0 {
		o.Hangover = 400 * time.Millisecond
	}
	if o.MinSpeech <= 0 {
		o.MinSpeech = 200 * time.Millisecond
	}
	if o.Pad <= 0 {
		o.Pad = 150 * time.Millisecond
	}
	if o.MinRMS <= 0 {
		o.MinRMS = 0.005
	}
	return o
}

// DetectSpeech finds speech in a recording. Unlike SpeechActivity it uses a
// two-threshold (hysteresis) detector against a 10th percentile noise floor,
// bridges short pauses (a hangover) so one sentence is one segment, and pads
// each segment so word onsets are not clipped.
func DetectSpeech(samples []float64, rate int, opt VADOptions) []SpeechSegment {
	opt = opt.withDefaults()
	frame := int(float64(rate) * opt.Frame.Seconds())
	if frame < 1 || len(samples) < rate/4 {
		return nil
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
	sort.Float64s(sorted)
	floor := sorted[len(sorted)/10]
	if floor < opt.MinRMS/3 {
		floor = opt.MinRMS / 3
	}
	start := math.Max(floor*opt.StartRatio, opt.MinRMS)
	end := math.Max(floor*opt.EndRatio, opt.MinRMS*0.66)
	step := opt.Frame.Seconds()
	hang := int(opt.Hangover / opt.Frame)

	var segs []SpeechSegment
	open, first, last := false, 0, 0
	for i, v := range rms {
		switch {
		case !open && v >= start:
			open, first, last = true, i, i
		case open && v >= end:
			last = i
		case open && i-last > hang:
			segs = append(segs, SpeechSegment{Start: float64(first) * step, End: float64(last+1) * step})
			open = false
		}
	}
	if open {
		segs = append(segs, SpeechSegment{Start: float64(first) * step, End: float64(last+1) * step})
	}
	total := float64(len(samples)) / float64(rate)
	var out []SpeechSegment
	for _, s := range segs {
		if s.End-s.Start < opt.MinSpeech.Seconds() {
			continue
		}
		s.Start = math.Max(0, s.Start-opt.Pad.Seconds())
		s.End = math.Min(total, s.End+opt.Pad.Seconds())
		if n := len(out); n > 0 && s.Start <= out[n-1].End {
			out[n-1].End = s.End
			continue
		}
		out = append(out, s)
	}
	return out
}

// Chunk is a stretch of audio that is transcribed on its own.
type Chunk struct {
	Start, End float64 // seconds into the recording
	Samples    []float64
}

// PlanChunks groups speech segments into chunks no longer than maxSeconds,
// cutting only at the pauses between segments (a single segment longer than
// the limit is cut at its quietest point). Silence between segments is
// dropped, so whisper never sees long stretches of noise it could
// hallucinate on.
func PlanChunks(samples []float64, rate int, segs []SpeechSegment, maxSeconds float64) []Chunk {
	if maxSeconds <= 0 {
		maxSeconds = 25
	}
	var chunks []Chunk
	var cur []SpeechSegment
	flush := func() {
		if len(cur) == 0 {
			return
		}
		var buf []float64
		for i, s := range cur {
			a, b := int(s.Start*float64(rate)), int(s.End*float64(rate))
			// Segments from DetectSpeech always lie inside the audio, but
			// PlanChunks is exported: clamp hand-built input instead of
			// slicing out of range.
			if a < 0 {
				a = 0
			}
			if a > len(samples) {
				a = len(samples)
			}
			if b > len(samples) {
				b = len(samples)
			}
			if b < a {
				b = a
			}
			if i > 0 { // 150 ms of silence between joined segments
				buf = append(buf, make([]float64, rate*3/20)...)
			}
			buf = append(buf, samples[a:b]...)
		}
		chunks = append(chunks, Chunk{Start: cur[0].Start, End: cur[len(cur)-1].End, Samples: buf})
		cur = nil
	}
	length := func(list []SpeechSegment) float64 {
		t := 0.0
		for _, s := range list {
			t += s.End - s.Start
		}
		return t
	}
	for _, s := range splitLong(samples, rate, segs, maxSeconds) {
		if len(cur) > 0 && length(cur)+(s.End-s.Start) > maxSeconds {
			flush()
		}
		cur = append(cur, s)
	}
	flush()
	return chunks
}

// splitLong cuts any segment longer than max at its quietest 20 ms frame in
// the second half of the allowed window.
func splitLong(samples []float64, rate int, segs []SpeechSegment, max float64) []SpeechSegment {
	var out []SpeechSegment
	frame := rate / 50
	total := float64(len(samples)) / float64(rate)
	for _, s := range segs {
		if s.Start < 0 {
			s.Start = 0
		}
		if s.End > total {
			s.End = total
		}
		if s.End <= s.Start {
			continue
		}
		for s.End-s.Start > max {
			lo := int((s.Start + max/2) * float64(rate))
			hi := int((s.Start + max) * float64(rate))
			best, bestE := lo, math.MaxFloat64
			for p := lo; p+frame <= hi && p+frame <= len(samples); p += frame {
				e := 0.0
				for _, v := range samples[p : p+frame] {
					e += v * v
				}
				if e < bestE {
					best, bestE = p, e
				}
			}
			cut := float64(best) / float64(rate)
			out = append(out, SpeechSegment{Start: s.Start, End: cut})
			s.Start = cut
		}
		out = append(out, s)
	}
	return out
}

// Result is a transcript with the quality information the UI shows.
type Result struct {
	Text       string  `json:"text"`
	Language   string  `json:"language,omitempty"`
	LangProb   float64 `json:"language_prob,omitempty"`
	Confidence float64 `json:"confidence"`         // 0..1, mean token probability; 0 means unknown (this path computed none), never an inferred score
	Model      string  `json:"model,omitempty"`    // model file that produced the text
	Fallback   bool    `json:"fallback,omitempty"` // true when a fallback backend/model won
	Backend    string  `json:"backend,omitempty"`  // backend identity, e.g. whisper.cpp or nvidia-nim/<model>; empty means unknown
	Chunks     int     `json:"chunks"`
	SpeechSec  float64 `json:"speech_sec"`
	Seconds    float64 `json:"seconds"`
}

// ASROptions extends Options with the quality settings.
type ASROptions struct {
	Options
	// FallbackModel is a second GGML model tried when the first one fails or
	// is unsure; empty disables the fallback.
	FallbackModel string
	// MinConfidence is the confidence under which the fallback is tried.
	MinConfidence float64
	// ChunkSeconds is the longest chunk handed to whisper.
	ChunkSeconds float64
	VAD          VADOptions
}

// DefaultASROptions reads the environment: SDRSTREAM_WHISPER_FALLBACK_MODEL
// and SDRSTREAM_WHISPER_MIN_CONF in addition to the usual variables.
func DefaultASROptions() ASROptions {
	o := ASROptions{Options: DefaultOptions(), MinConfidence: 0.8, ChunkSeconds: 25}
	o.FallbackModel = os.Getenv("SDRSTREAM_WHISPER_FALLBACK_MODEL")
	if v, err := strconv.ParseFloat(os.Getenv("SDRSTREAM_WHISPER_MIN_CONF"), 64); err == nil && v > 0 {
		o.MinConfidence = v
	}
	if o.FallbackModel == "" {
		// Use the next larger model in the cache, if there is one.
		primary := FindModel(o.Options)
		matches, _ := filepath.Glob(filepath.Join(CacheDir, "ggml-*.bin"))
		sortByPreference(matches)
		for _, m := range matches {
			if m != primary && primary != "" && sizeOf(m) > sizeOf(primary) {
				o.FallbackModel = m
				break
			}
		}
	}
	return o
}

func sizeOf(path string) int64 {
	if info, err := os.Stat(path); err == nil {
		return info.Size()
	}
	return 0
}

// wjson is the part of whisper-cli's -ojf output that is used here.
type wjson struct {
	Result struct {
		Language string `json:"language"`
	} `json:"result"`
	Transcription []struct {
		Text   string `json:"text"`
		Tokens []struct {
			Text string  `json:"text"`
			P    float64 `json:"p"`
		} `json:"tokens"`
	} `json:"transcription"`
}

// ParseWhisperJSON extracts the text, language and confidence from the file
// written by whisper-cli -ojf. Special tokens ([_BEG_], [_TT_n]) are not
// counted. Confidence is the mean probability of the text tokens.
func ParseWhisperJSON(data []byte) (text, lang string, confidence float64, err error) {
	var w wjson
	if err = json.Unmarshal(data, &w); err != nil {
		return
	}
	lang = w.Result.Language
	sum, n := 0.0, 0
	var parts []string
	for _, seg := range w.Transcription {
		parts = append(parts, seg.Text)
		for _, t := range seg.Tokens {
			if strings.HasPrefix(t.Text, "[_") {
				continue
			}
			sum += t.P
			n++
		}
	}
	text = cleanTranscript(strings.Join(parts, " "))
	if n > 0 {
		confidence = sum / float64(n)
	}
	return
}

// runWhisper transcribes one 16 kHz WAV with one model.
func runWhisper(ctx context.Context, binary, model, wav string, o Options) (text, lang string, conf float64, err error) {
	// The quality pipeline shares the one local whisper slot with
	// Transcribe: each chunk forks the same multi-GB process, and an
	// unbounded caller multiplied the resident model per concurrent run.
	// Acquired before the run timeout so queueing callers do not burn
	// their inference budget while waiting.
	select {
	case transcribeSlot <- struct{}{}:
		defer func() { <-transcribeSlot }()
	case <-ctx.Done():
		return "", "", 0, ctx.Err()
	}
	base := strings.TrimSuffix(wav, ".wav")
	args := append(whisperArgs(model, wav, o), "-ojf", "-of", base)
	runCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(runCtx, binary, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if e := cmd.Run(); e != nil {
		return "", "", 0, fmt.Errorf("voice: %v: %s", e, tail(stderr.String(), 200))
	}
	defer os.Remove(base + ".json")
	data, e := os.ReadFile(base + ".json")
	if e != nil {
		return "", "", 0, e
	}
	return ParseWhisperJSON(data)
}

// hallucination reports text that whisper is known to invent on noise.
// Single-word fillers are real speech often enough that they only fail at
// low confidence; marker-only output ([music], (applause), ...) and the
// guard's longer canned YouTube phrases are never broadcast content, so
// they fail at any confidence, exactly as the publish-time Guard rejects
// them (guard.go). The two filters share one definition on purpose.
func hallucination(text string, conf float64) bool {
	t := strings.ToLower(strings.Trim(strings.TrimSpace(text), ".!? "))
	switch t {
	case "", "you", "thank you", "bye", "thanks", "the", "uh", "um":
		return conf < 0.8
	}
	if stripMarkers(t) == "" || isCanned(t) {
		return true
	}
	// The same word repeated over and over is a decoder loop.
	words := strings.Fields(t)
	if len(words) >= 6 {
		seen := map[string]int{}
		for _, w := range words {
			seen[w]++
		}
		for _, c := range seen {
			if float64(c) > 0.7*float64(len(words)) {
				return true
			}
		}
	}
	return false
}

// TranscribeASR is the quality pipeline: detect speech, drop the silence,
// chunk at pauses, transcribe each chunk, fall back to a second local model
// for chunks the first model is unsure about, and report language and
// confidence. Text from chunks that look like hallucinations is dropped.
func TranscribeASR(ctx context.Context, samples []float64, rate int, o ASROptions) (Result, error) {
	res := Result{Seconds: float64(len(samples)) / float64(rate)}
	binary, model := FindBinary(o.Options), FindModel(o.Options)
	if binary == "" || model == "" {
		return res, fmt.Errorf("voice: no whisper.cpp binary or model found")
	}
	segs := DetectSpeech(samples, rate, o.VAD)
	if len(segs) == 0 {
		return res, nil // no speech: say nothing rather than guess
	}
	for _, s := range segs {
		res.SpeechSec += s.End - s.Start
	}
	chunks := PlanChunks(samples, rate, segs, o.ChunkSeconds)
	res.Chunks = len(chunks)
	var texts []string
	var confSum, confW float64
	langVotes := map[string]float64{}
	for _, ch := range chunks {
		weight := float64(len(ch.Samples))
		pcm := Resample(ch.Samples, rate, 16000)
		file, err := os.CreateTemp("", "sdrstream-asr-*.wav")
		if err != nil {
			return res, err
		}
		file.Close()
		wav := file.Name()
		err = WriteWAV(wav, pcm, 16000)
		var text, lang string
		var conf float64
		usedModel, fell := model, false
		if err == nil {
			text, lang, conf, err = runWhisper(ctx, binary, model, wav, o.Options)
		}
		if (err != nil || conf < o.MinConfidence || hallucination(text, conf)) && o.FallbackModel != "" && o.FallbackModel != model {
			t2, l2, c2, e2 := runWhisper(ctx, binary, o.FallbackModel, wav, o.Options)
			// Selection contract: MinConfidence is the retry trigger, not a
			// publishing floor. Validity ranks before confidence - a known
			// hallucination's confidence is the invention's confidence and
			// says nothing - so a valid candidate always beats an invented
			// one, and confidence only chooses between two valid candidates.
			// A survivor below MinConfidence is published with its honest
			// low confidence for the UI chip and the publish-time Guard to
			// judge; dropping it would silence real speech.
			cleanBeatsHallucination := hallucination(text, conf) && !hallucination(t2, c2)
			// Symmetrically, a valid primary never yields to an invented
			// fallback. When the primary run failed outright the fallback is
			// taken regardless, so one bad chunk cannot error the whole
			// transcription; an invented fallback is then skipped below.
			fallbackInvented := err == nil && hallucination(t2, c2)
			if e2 == nil && (err != nil || !fallbackInvented && (c2 > conf || cleanBeatsHallucination)) {
				text, lang, conf, err, usedModel, fell = t2, l2, c2, nil, o.FallbackModel, true
			}
		}
		os.Remove(wav)
		if err != nil {
			return res, err
		}
		if hallucination(text, conf) {
			continue
		}
		if text != "" {
			texts = append(texts, text)
			confSum += conf * weight
			confW += weight
			if lang != "" {
				langVotes[lang] += weight
			}
			if fell {
				res.Fallback = true
			}
			res.Model = filepath.Base(usedModel)
		}
	}
	res.Text = strings.Join(texts, " ")
	if confW > 0 {
		res.Confidence = confSum / confW
	}
	best := 0.0
	var total float64
	for _, v := range langVotes {
		total += v
	}
	for l, v := range langVotes {
		if v > best {
			best, res.Language = v, l
		}
	}
	if total > 0 {
		res.LangProb = best / total
	}
	return res, nil
}
