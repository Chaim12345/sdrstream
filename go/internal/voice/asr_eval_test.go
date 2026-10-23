package voice

// Durable real-audio WER regression harness. It runs the real recognizer
// (whisper.cpp, hash-pinned models) over a hash-pinned speech corpus with
// reference transcripts and compares measured WER against the gates in
// testdata/eval/manifest.json. Everything is skip-gated: without the corpus
// (SDRSTREAM_ASR_EVAL_DIR), the pinned models (SDRSTREAM_WHISPER_MODEL /
// SDRSTREAM_WHISPER_FALLBACK_MODEL) or a whisper-cli binary the tests skip
// instead of failing. Hash mismatches in present material FAIL - a corrupt
// or swapped corpus is a real finding.
//
// Measured values and gate margins were recorded once at manifest creation
// (see manifest "purpose"); per project rule a failing gate is REPORTED,
// never fixed by tuning the gate.
//
// normalise/wer/biquad/radioChannel below duplicate cmd/asreval's helpers on
// purpose: the harness must not touch production or tool code, and the
// duplication is pinned by the manifest's reproduced numbers.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/chaim12345/sdrstream/go/internal/decode"
)

type evalManifest struct {
	Version          int    `json:"version"`
	WhisperCppCommit string `json:"whisper_cpp_commit"`
	Models           struct {
		Primary  struct{ File, SHA256 string } `json:"primary"`
		Fallback struct {
			File, SHA256  string
			MinConfidence float64 `json:"min_confidence"`
		} `json:"fallback"`
	} `json:"models"`
	Files []struct {
		Name      string `json:"name"`
		WavSHA256 string `json:"wav_sha256"`
		TxtSHA256 string `json:"txt_sha256"`
		RefWords  int    `json:"ref_words"`
	} `json:"files"`
	Conditions []struct {
		Name     string   `json:"name"`
		SnrDB    *float64 `json:"snr_db"`
		GapS     float64  `json:"gap_s"`
		Measured struct {
			BaselineWER float64 `json:"baseline_wer_pct"`
			AsrWER      float64 `json:"asr_wer_pct"`
		} `json:"measured"`
		GateAbsPts float64 `json:"gate_abs_pts"`
	} `json:"conditions"`
}

func loadEvalManifest(t *testing.T) *evalManifest {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "eval", "manifest.json"))
	if err != nil {
		t.Fatalf("read eval manifest: %v", err)
	}
	var m evalManifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse eval manifest: %v", err)
	}
	if m.Version != 1 {
		t.Fatalf("manifest version = %d, want 1", m.Version)
	}
	return &m
}

func sha256File(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		t.Fatalf("hash %s: %v", path, err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// evalSetup resolves and hash-pins the corpus and models, skipping when any
// piece is absent.
func evalSetup(t *testing.T, m *evalManifest) (corpusDir string, asr ASROptions, base Options) {
	t.Helper()
	corpusDir = os.Getenv("SDRSTREAM_ASR_EVAL_DIR")
	if corpusDir == "" {
		t.Skip("no eval corpus: fetch it with scripts/fetch-asr-eval-corpus.sh and set SDRSTREAM_ASR_EVAL_DIR")
	}
	base = DefaultOptions()
	if FindBinary(base) == "" {
		t.Skip("no whisper-cli binary (set SDRSTREAM_WHISPER)")
	}
	primary, fallback := FindModel(base), os.Getenv("SDRSTREAM_WHISPER_FALLBACK_MODEL")
	if primary == "" || fallback == "" {
		t.Skip("need SDRSTREAM_WHISPER_MODEL and SDRSTREAM_WHISPER_FALLBACK_MODEL pointing at the pinned models")
	}
	if got := sha256File(t, primary); got != m.Models.Primary.SHA256 {
		t.Fatalf("primary model %s sha256 = %.16s, manifest pins %.16s", primary, got, m.Models.Primary.SHA256)
	}
	if got := sha256File(t, fallback); got != m.Models.Fallback.SHA256 {
		t.Fatalf("fallback model %s sha256 = %.16s, manifest pins %.16s", fallback, got, m.Models.Fallback.SHA256)
	}
	for _, f := range m.Files {
		if got := sha256File(t, filepath.Join(corpusDir, f.Name+".wav")); got != f.WavSHA256 {
			t.Fatalf("corpus %s.wav sha256 mismatch (got %.16s, pinned %.16s)", f.Name, got, f.WavSHA256)
		}
		if got := sha256File(t, filepath.Join(corpusDir, f.Name+".txt")); got != f.TxtSHA256 {
			t.Fatalf("corpus %s.txt sha256 mismatch (got %.16s, pinned %.16s)", f.Name, got, f.TxtSHA256)
		}
	}
	asr = DefaultASROptions()
	asr.FallbackModel = fallback
	return corpusDir, asr, base
}

var evalNonWord = regexp.MustCompile(`[^a-z0-9' ]+`)

func evalNormalise(s string) []string {
	s = strings.ToLower(strings.ReplaceAll(s, "-", " "))
	return strings.Fields(evalNonWord.ReplaceAllString(s, " "))
}

// evalWER is the word error rate: edit distance over reference length.
func evalWER(ref, hyp []string) (errs, n int) {
	d := make([][]int, len(ref)+1)
	for i := range d {
		d[i] = make([]int, len(hyp)+1)
		d[i][0] = i
	}
	for j := range d[0] {
		d[0][j] = j
	}
	for i := 1; i <= len(ref); i++ {
		for j := 1; j <= len(hyp); j++ {
			c := 1
			if ref[i-1] == hyp[j-1] {
				c = 0
			}
			d[i][j] = min(d[i-1][j-1]+c, min(d[i-1][j]+1, d[i][j-1]+1))
		}
	}
	return d[len(ref)][len(hyp)], len(ref)
}

type evalBiquad struct{ b0, b1, b2, a1, a2, x1, x2, y1, y2 float64 }

func (q *evalBiquad) run(x float64) float64 {
	y := q.b0*x + q.b1*q.x1 + q.b2*q.x2 - q.a1*q.y1 - q.a2*q.y2
	q.x2, q.x1, q.y2, q.y1 = q.x1, x, q.y1, y
	return y
}

func evalRBJ(kind string, f0, rate, qf float64) *evalBiquad {
	w := 2 * math.Pi * f0 / rate
	alpha := math.Sin(w) / (2 * qf)
	var b0, b1, b2 float64
	if kind == "hp" {
		b0, b1, b2 = (1+math.Cos(w))/2, -(1 + math.Cos(w)), (1+math.Cos(w))/2
	} else {
		b0, b1, b2 = (1-math.Cos(w))/2, 1-math.Cos(w), (1-math.Cos(w))/2
	}
	a0 := 1 + alpha
	return &evalBiquad{b0: b0 / a0, b1: b1 / a0, b2: b2 / a0, a1: -2 * math.Cos(w) / a0, a2: (1 - alpha) / a0}
}

// evalRadioChannel band-limits to 300-2800 Hz, adds noise at snr dB relative
// to the speech RMS and surrounds the clip with noise-only audio. This is a
// SIMULATED channel, never a real radio recording.
func evalRadioChannel(x []float64, rate int, snr float64, lead, tail float64, rng *rand.Rand) []float64 {
	hp, lp := evalRBJ("hp", 300, float64(rate), 0.707), evalRBJ("lp", 2800, float64(rate), 0.707)
	y := make([]float64, len(x))
	e := 0.0
	for i, v := range x {
		y[i] = lp.run(hp.run(v))
		e += y[i] * y[i]
	}
	sigma := math.Sqrt(e/float64(len(y))) / math.Pow(10, snr/20)
	out := make([]float64, 0, len(y)+int((lead+tail)*float64(rate)))
	out = append(out, make([]float64, int(lead*float64(rate)))...)
	out = append(out, y...)
	out = append(out, make([]float64, int(tail*float64(rate)))...)
	for i := range out {
		out[i] += rng.NormFloat64() * sigma
	}
	return out
}

// TestASREvalCorpus reproduces the manifest's WER numbers with the pinned
// recognizer and corpus: the ASR pipeline (VAD/chunking/fallback) and the
// plain baseline, clean and through the simulated radio channel.
func TestASREvalCorpus(t *testing.T) {
	m := loadEvalManifest(t)
	corpusDir, asr, base := evalSetup(t, m)
	refs := map[string][]string{}
	audio := map[string][]float64{}
	rate := 0
	for _, f := range m.Files {
		raw, err := os.ReadFile(filepath.Join(corpusDir, f.Name+".txt"))
		if err != nil {
			t.Fatalf("read ref %s: %v", f.Name, err)
		}
		refs[f.Name] = evalNormalise(string(raw))
		x, r, err := decode.ReadWAV(filepath.Join(corpusDir, f.Name+".wav"))
		if err != nil {
			t.Fatalf("read %s: %v", f.Name, err)
		}
		audio[f.Name], rate = x, r
	}
	names := make([]string, 0, len(m.Files))
	for _, f := range m.Files {
		names = append(names, f.Name)
	}
	sort.Strings(names)

	for _, cond := range m.Conditions {
		// One seeded RNG per condition over sorted files reproduces the
		// recorded noise sequences exactly (clean consumes no draws).
		rng := rand.New(rand.NewSource(7))
		baseErrs, asrErrs, words := 0, 0, 0
		for _, name := range names {
			x := audio[name]
			if cond.SnrDB != nil {
				x = evalRadioChannel(x, rate, *cond.SnrDB, cond.GapS, cond.GapS, rng)
			}
			ref := refs[name]
			old, err := Transcribe(context.Background(), x, rate, base)
			if err != nil {
				t.Fatalf("%s/%s baseline: %v", cond.Name, name, err)
			}
			e, n := evalWER(ref, evalNormalise(old.Text))
			r, err := TranscribeASR(context.Background(), x, rate, asr)
			if err != nil {
				t.Fatalf("%s/%s asr: %v", cond.Name, name, err)
			}
			e2, _ := evalWER(ref, evalNormalise(r.Text))
			baseErrs, asrErrs, words = baseErrs+e, asrErrs+e2, words+n
			t.Logf("%-10s %-22s base_err=%2d asr_err=%2d/%2d conf=%.2f lang=%s fb=%v",
				cond.Name, name, e, e2, n, r.Confidence, r.Language, r.Fallback)
		}
		baseWER := 100 * float64(baseErrs) / float64(words)
		asrWER := 100 * float64(asrErrs) / float64(words)
		t.Logf("%s: baseline WER %.1f%% (%d/%d), asr WER %.1f%% (%d/%d), manifest %.1f/%.1f gate +%.1f",
			cond.Name, baseWER, baseErrs, words, asrWER, asrErrs, words,
			cond.Measured.BaselineWER, cond.Measured.AsrWER, cond.GateAbsPts)
		if math.Abs(baseWER-cond.Measured.BaselineWER) > cond.GateAbsPts {
			t.Errorf("%s baseline WER %.1f%% outside gate %.1f%% + %.1f pts", cond.Name, baseWER, cond.Measured.BaselineWER, cond.GateAbsPts)
		}
		if math.Abs(asrWER-cond.Measured.AsrWER) > cond.GateAbsPts {
			t.Errorf("%s asr WER %.1f%% outside gate %.1f%% + %.1f pts", cond.Name, asrWER, cond.Measured.AsrWER, cond.GateAbsPts)
		}
	}
}

// TestVolmetProductionPathBehavior pins what the PRODUCTION monitor path
// (TranscribeBest -> GuardTranscript, no NIM key) does with the in-repo real
// HF SSB recording under the pinned tiny.en model: whisper labels it
// non-speech and the publish-time Guard rejects it, so nothing is published.
// This pins observed behavior, not correctness: with a different model the
// outcome can legitimately change, and a failure here is REPORTED, not tuned
// away. Reference-free: no WER claim is possible for this clip.
func TestVolmetProductionPathBehavior(t *testing.T) {
	m := loadEvalManifest(t)
	base := DefaultOptions()
	if FindBinary(base) == "" || FindModel(base) == "" {
		t.Skip("no whisper-cli binary or pinned model")
	}
	if got := sha256File(t, FindModel(base)); got != m.Models.Primary.SHA256 {
		t.Skip("primary model is not the manifest-pinned tiny.en; behavior pin would not be comparable")
	}
	samples, rate, err := decode.ReadWAV(filepath.Join("..", "..", "..", "tests", "data", "real_volmet_shannon_5505_usb.wav"))
	if err != nil {
		t.Fatalf("read VOLMET recording: %v", err)
	}
	tr, err := TranscribeBest(context.Background(), samples, rate, base, DefaultNIMOptions())
	if err != nil {
		t.Fatalf("TranscribeBest: %v", err)
	}
	verdict := GuardTranscript(samples, rate, tr.Text)
	t.Logf("production path on real HF SSB: text=%q rejected=%v reason=%q", tr.Text, verdict.Rejected, verdict.Reason)
	if !verdict.Rejected && strings.TrimSpace(verdict.Text) != "" {
		t.Errorf("production path would publish %q for the VOLMET clip; previously the Guard rejected the marker-only output - behavior change to report", verdict.Text)
	}
}
