// Command asreval measures transcription quality on a folder of reference
// recordings. Each NAME.wav needs a NAME.txt with the true words.
//
// It compares the old path (voice.Transcribe on the whole file) with the
// new one (voice.TranscribeASR) on the clean file and on a degraded copy
// that imitates a narrowband radio channel (300-2800 Hz band-pass, white
// noise at a given SNR, noise-only lead-in and tail). The degraded copy is
// SIMULATED: it is real speech through a synthetic channel, not a real
// radio recording.
package main

import (
	"context"
	"flag"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/decode"
	"github.com/chaim12345/sdrstream/go/internal/voice"
)

var nonWord = regexp.MustCompile(`[^a-z0-9' ]+`)

func normalise(s string) []string {
	s = strings.ToLower(strings.ReplaceAll(s, "-", " "))
	return strings.Fields(nonWord.ReplaceAllString(s, " "))
}

// wer is the word error rate: edit distance over reference length.
func wer(ref, hyp []string) (errs, n int) {
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

type biquad struct{ b0, b1, b2, a1, a2, x1, x2, y1, y2 float64 }

func (q *biquad) run(x float64) float64 {
	y := q.b0*x + q.b1*q.x1 + q.b2*q.x2 - q.a1*q.y1 - q.a2*q.y2
	q.x2, q.x1, q.y2, q.y1 = q.x1, x, q.y1, y
	return y
}

func rbj(kind string, f0, rate, qf float64) *biquad {
	w := 2 * math.Pi * f0 / rate
	alpha := math.Sin(w) / (2 * qf)
	var b0, b1, b2 float64
	if kind == "hp" {
		b0, b1, b2 = (1+math.Cos(w))/2, -(1 + math.Cos(w)), (1+math.Cos(w))/2
	} else {
		b0, b1, b2 = (1-math.Cos(w))/2, 1-math.Cos(w), (1-math.Cos(w))/2
	}
	a0 := 1 + alpha
	return &biquad{b0: b0 / a0, b1: b1 / a0, b2: b2 / a0, a1: -2 * math.Cos(w) / a0, a2: (1 - alpha) / a0}
}

// radioChannel band-limits to 300-2800 Hz, adds noise at snr dB relative to
// the speech RMS and surrounds the clip with noise-only audio.
func radioChannel(x []float64, rate int, snr float64, lead, tail float64, rng *rand.Rand) []float64 {
	hp, lp := rbj("hp", 300, float64(rate), 0.707), rbj("lp", 2800, float64(rate), 0.707)
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

func main() {
	dir := flag.String("dir", "", "folder of NAME.wav + NAME.txt")
	snr := flag.Float64("snr", 10, "SNR in dB for the simulated radio channel")
	gap := flag.Float64("gap", 3, "seconds of noise-only audio before and after the speech")
	limit := flag.Int("n", 0, "use at most this many files (0 = all)")
	flag.Parse()
	files, _ := filepath.Glob(filepath.Join(*dir, "*.wav"))
	sort.Strings(files)
	if *limit > 0 && len(files) > *limit {
		files = files[:*limit]
	}
	asr := voice.DefaultASROptions()
	base := voice.DefaultOptions()
	fmt.Printf("primary=%s fallback=%s min_conf=%.2f\n", filepath.Base(voice.FindModel(base)), filepath.Base(asr.FallbackModel), asr.MinConfidence)
	type acc struct {
		errs, n int
		dur     time.Duration
	}
	stats := map[string]*acc{}
	add := func(k string, e, n int, d time.Duration) {
		if stats[k] == nil {
			stats[k] = &acc{}
		}
		stats[k].errs += e
		stats[k].n += n
		stats[k].dur += d
	}
	var confOK, confBad []float64
	rng := rand.New(rand.NewSource(7))
	for _, f := range files {
		refRaw, err := os.ReadFile(strings.TrimSuffix(f, ".wav") + ".txt")
		if err != nil {
			continue
		}
		ref := normalise(string(refRaw))
		x, rate, err := decode.ReadWAV(f)
		if err != nil {
			continue
		}
		for _, v := range []struct {
			name string
			s    []float64
		}{{"clean", x}, {fmt.Sprintf("radio%+.0fdB", *snr), radioChannel(x, rate, *snr, *gap, *gap, rng)}} {
			t0 := time.Now()
			old, _ := voice.Transcribe(context.Background(), v.s, rate, base)
			e, n := wer(ref, normalise(old.Text))
			add(v.name+"/baseline", e, n, time.Since(t0))
			t0 = time.Now()
			r, _ := voice.TranscribeASR(context.Background(), v.s, rate, asr)
			e2, _ := wer(ref, normalise(r.Text))
			add(v.name+"/asr", e2, n, time.Since(t0))
			w := float64(e2) / float64(n)
			if w <= 0.1 {
				confOK = append(confOK, r.Confidence)
			} else {
				confBad = append(confBad, r.Confidence)
			}
			fmt.Printf("%-12s %-14s base_err=%2d asr_err=%2d/%2d conf=%.2f lang=%s fb=%v\n",
				v.name, filepath.Base(f), e, e2, n, r.Confidence, r.Language, r.Fallback)
		}
	}
	mean := func(v []float64) float64 {
		if len(v) == 0 {
			return math.NaN()
		}
		s := 0.0
		for _, x := range v {
			s += x
		}
		return s / float64(len(v))
	}
	var keys []string
	for k := range stats {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Println("\ncondition/system       WER     words  seconds")
	for _, k := range keys {
		s := stats[k]
		fmt.Printf("%-20s %6.1f%%  %5d  %6.1f\n", k, 100*float64(s.errs)/float64(max(1, s.n)), s.n, s.dur.Seconds())
	}
	fmt.Printf("\nmean confidence: files with WER<=10%%: %.2f (%d)   files with WER>10%%: %.2f (%d)\n",
		mean(confOK), len(confOK), mean(confBad), len(confBad))
}
