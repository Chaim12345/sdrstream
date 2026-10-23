package decode

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
)

// Morse decodes keyed-tone CW: it tracks the dominant tone's on/off
// envelope, infers the dot/dash element from the distribution of run
// lengths, and applies standard Farnsworth spacing rules for letters and
// words. It also reports the tone frequency found, so a beacon can be
// distinguished from a local spur (which would sit at a constant,
// Doppler-free frequency).
type MorseResult struct {
	Text      string
	ToneHz    float64
	SnrdB     float64
	DotsFound int
	DotMs     float64
}

var morseTable = map[string]string{
	".-": "A", "-...": "B", "-.-.": "C", "-..": "D", ".": "E", "..-.": "F",
	"--.": "G", "....": "H", "..": "I", ".---": "J", "-.-": "K", ".-..": "L",
	"--": "M", "-.": "N", "---": "O", ".--.": "P", "--.-": "Q", ".-.": "R",
	"...": "S", "-": "T", "..-": "U", "...-": "V", ".--": "W", "-..-": "X",
	"-.--": "Y", "--..": "Z", "-----": "0", ".----": "1", "..---": "2",
	"...--": "3", "....-": "4", ".....": "5", "-....": "6", "--...": "7",
	"---..": "8", "----.": "9", "-..-.": "/", "..--..": "?", ".-.-.-": ".",
	"--..--": ",", "---...": ":", ".----.": "'", "-....-": "-", ".-.-.": "+",
	"..--.-": "_", ".--.-.": "@", "-...-": "=", ".-..-.": "\"", ".-...": "&",
	"-.--.": "(", "-.--.-": ")", "...-..-": "$", "..-.-": "\u00a1", "--.-.": "\u00bf",
	"-.-.--": "!", "...-.": "\u2116",
}

// FindDominantTone reports the strongest spectral peak between 300 and
// 3000 Hz, the beacon hunting ground for SSB captures.
func FindDominantTone(samples []float64, rate int) (hz float64, snrDB float64) {
	// Coarse Goertzel scan over 300-3000 Hz in 5 Hz steps, then refine.
	const lo, hi, step = 300.0, 3000.0, 5.0
	bestHz, bestMag := 0.0, -1.0
	for f := lo; f <= hi; f += step {
		mag := goertzel(samples, rate, f)
		if mag > bestMag {
			bestMag, bestHz = mag, f
		}
	}
	if bestHz == 0 {
		return 0, math.Inf(-1)
	}
	// Refine +/- 5 Hz at 1 Hz resolution.
	refined, refMag := bestHz, bestMag
	for f := bestHz - step; f <= bestHz+step; f += 1 {
		if f <= 0 {
			continue
		}
		mag := goertzel(samples, rate, f)
		if mag > refMag {
			refined, refMag = f, mag
		}
	}
	// Noise floor = median magnitude across the band.
	var mags []float64
	for f := lo; f <= hi; f += 20 {
		mags = append(mags, goertzel(samples, rate, f))
	}
	sort.Float64s(mags)
	floor := mags[len(mags)/2]
	if floor <= 0 {
		floor = 1e-12
	}
	return refined, 10 * math.Log10(refMag/floor)
}

func goertzel(samples []float64, rate int, freq float64) float64 {
	k := 2 * math.Pi * freq / float64(rate)
	coeff := 2 * math.Cos(k)
	var s0, s1, s2 float64
	for _, x := range samples {
		s0 = x + coeff*s1 - s2
		s2 = s1
		s1 = s0
	}
	return s1*s1 + s2*s2 - coeff*s1*s2
}

// MorseDecode runs envelope tracking over the whole file and returns any
// readable text plus tone diagnostics.
func MorseDecode(path string) (MorseResult, error) {
	return MorseDecodeContext(context.Background(), path)
}

// MorseDecodeContext is MorseDecode with cancellation checked per analysis
// window and per envelope pass: a cancelled context returns
// (MorseResult{}, ctx.Err()).
func MorseDecodeContext(ctx context.Context, path string) (MorseResult, error) {
	samples, rate, err := ReadWAV(path)
	if err != nil {
		return MorseResult{}, err
	}
	res, tone, snr, err := morseFromSamplesContext(ctx, samples, rate)
	if err != nil {
		return MorseResult{}, err
	}
	res.ToneHz, res.SnrdB = tone, snr
	return res, nil
}

// Plausible reports whether the read passes the noise-rejection gates
// (beacon-rate element timing and a mostly decodable character mix).
// morseFromSamples returns its best read even when implausible, so callers
// publishing transcripts must gate on this.
func (r MorseResult) Plausible() bool {
	return plausibleMorseRange(r, 2500)
}

// morseEnvelope tracks the keyed-on/off envelope at one tone frequency
// using 10 ms Goertzel magnitude windows.
func morseEnvelope(samples []float64, rate int, tone float64) []float64 {
	env, _ := morseEnvelopeContext(context.Background(), samples, rate, tone)
	return env
}

func morseEnvelopeContext(ctx context.Context, samples []float64, rate int, tone float64) ([]float64, error) {
	win := rate / 100
	if win < 1 {
		win = 1
	}
	var env []float64
	for start := 0; start+win <= len(samples); start += win {
		if len(env)%256 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		env = append(env, goertzel(samples[start:start+win], rate, tone))
	}
	return env, nil
}

// morseSecondTone finds the strongest tone at least 80 Hz away from the
// primary tone, used to follow two-tone MCW signals that alternate
// between two keyed carriers (numbers-station Morse such as M1).
func morseSecondTone(samples []float64, rate int, primary float64) float64 {
	const lo, hi, step = 300.0, 2600.0, 5.0
	bestHz, bestMag := 0.0, -1.0
	for f := lo; f <= hi; f += step {
		if math.Abs(f-primary) < 80 {
			continue
		}
		if mag := goertzel(samples, rate, f); mag > bestMag {
			bestMag, bestHz = mag, f
		}
	}
	if bestHz <= 0 {
		return 0
	}
	refined, refMag := bestHz, bestMag
	for f := bestHz - step; f <= bestHz+step; f += 1 {
		if f <= 0 || math.Abs(f-primary) < 80 {
			continue
		}
		if mag := goertzel(samples, rate, f); mag > refMag {
			refined, refMag = f, mag
		}
	}
	return refined
}

func morseLetters(text string) int {
	n := 0
	for _, r := range text {
		if r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			n++
		}
	}
	return n
}

func morseFromSamples(samples []float64, rate int) (MorseResult, float64, float64) {
	res, tone, snr, _ := morseFromSamplesContext(context.Background(), samples, rate)
	return res, tone, snr
}

func morseFromSamplesContext(ctx context.Context, samples []float64, rate int) (MorseResult, float64, float64, error) {
	// Analyse in 1 s windows: pick the strongest window's tone once, then
	// track the envelope at that frequency for the whole file.
	chunk := rate
	if len(samples) < chunk {
		chunk = len(samples)
	}
	bestTone, bestSNR := 0.0, math.Inf(-1)
	bestStart := 0
	for start := 0; start+chunk <= len(samples); start += chunk {
		if err := ctx.Err(); err != nil {
			return MorseResult{}, 0, 0, err
		}
		f, snr := FindDominantTone(samples[start:start+chunk], rate)
		if snr > bestSNR {
			bestTone, bestSNR = f, snr
			bestStart = start
		}
	}
	if bestTone <= 0 {
		return MorseResult{Text: ""}, 0, bestSNR, nil
	}

	env, err := morseEnvelopeContext(ctx, samples, rate, bestTone)
	if err != nil {
		return MorseResult{}, 0, 0, err
	}
	if len(env) < 3 {
		return MorseResult{Text: ""}, bestTone, bestSNR, nil
	}
	res := morseFromEnvelope(env, rate, bestTone, bestSNR, 2000)
	if plausibleMorse(res) {
		return res, bestTone, bestSNR, nil
	}

	// Two-tone fallback: numbers-station MCW alternates the keying
	// between two carriers, so a single-tone envelope sees only half the
	// elements. Combine both envelopes and try again with a wider tone
	// ceiling. Only a strictly better read replaces the first attempt.
	if tone2 := morseSecondTone(samples[bestStart:bestStart+chunk], rate, bestTone); tone2 > 0 {
		env2, err := morseEnvelopeContext(ctx, samples, rate, tone2)
		if err != nil {
			return MorseResult{}, 0, 0, err
		}
		if len(env2) == len(env) {
			comb := make([]float64, len(env))
			for i := range comb {
				if env2[i] > env[i] {
					comb[i] = env2[i]
				} else {
					comb[i] = env[i]
				}
			}
			if res2 := morseFromEnvelope(comb, rate, bestTone, bestSNR, 2500); plausibleMorseRange(res2, 2500) && morseLetters(res2.Text) > morseLetters(res.Text) {
				return res2, bestTone, bestSNR, nil
			}
		}
	}
	return res, bestTone, bestSNR, nil
}

// morseFromEnvelope turns a keyed-tone envelope into text. maxToneHz is
// the plausibility ceiling for the reported tone frequency.
func morseFromEnvelope(env []float64, rate int, bestTone, bestSNR, maxToneHz float64) MorseResult {
	win := rate / 100
	if win < 1 {
		win = 1
	}
	sort2 := make([]float64, len(env))
	copy(sort2, env)
	sort.Float64s(sort2)
	floor := sort2[len(sort2)/4] // noise floor from the quietest quartile
	peak := sort2[3*len(sort2)/4]
	if peak <= floor {
		return MorseResult{Text: "", ToneHz: bestTone, SnrdB: bestSNR}
	}
	threshold := math.Sqrt(floor * peak)

	// Run lengths of keyed/off, in envelope units (10 ms each).
	var runs []bool   // true = keyed
	var lengths []int // in envelope steps
	keyed := env[0] > threshold
	count := 1
	for i := 1; i < len(env); i++ {
		now := env[i] > threshold
		if now == keyed {
			count++
			continue
		}
		runs = append(runs, keyed)
		lengths = append(lengths, count)
		keyed, count = now, 1
	}
	runs = append(runs, keyed)
	lengths = append(lengths, count)

	// Element length: the shortest common keyed run (dots). Use the median
	// of all keyed runs below 3x the minimum.
	var onLens []int
	for i, r := range runs {
		if r {
			onLens = append(onLens, lengths[i])
		}
	}
	if len(onLens) == 0 {
		return MorseResult{Text: "", ToneHz: bestTone, SnrdB: bestSNR}
	}
	sort.Ints(onLens)
	dot := onLens[0]
	// Cluster: dots vs dashes by the gap in the length distribution.
	var dash int
	if n := len(onLens); n >= 4 {
		mid := n / 2
		if onLens[mid] > onLens[0]*2 {
			// Bimodal; dots are the lower cluster.
			var upper []int
			lower := onLens[:mid]
			for _, v := range onLens[mid:] {
				if v >= lower[len(lower)-1]*2 {
					upper = append(upper, v)
				}
			}
			if len(upper) > 0 {
				sort.Ints(upper)
				dash = upper[len(upper)/2]
			}
		}
	}
	if dash == 0 {
		dash = dot * 3
	}

	// Translate runs into text with adaptive timing.
	var sb strings.Builder
	var symbol strings.Builder
	var dots int
	flushSymbol := func() {
		if symbol.Len() == 0 {
			return
		}
		if ch, ok := morseTable[symbol.String()]; ok {
			sb.WriteString(ch)
		} else {
			sb.WriteString(fmt.Sprintf("<%s>", symbol.String()))
		}
		symbol.Reset()
	}
	for i, r := range runs {
		l := lengths[i]
		if r { // keyed: dot or dash
			if l <= dot*2 {
				symbol.WriteByte('.')
			} else {
				symbol.WriteByte('-')
			}
		} else {
			// Gap: intra-character, inter-character or word. Real
			// captures lose gap length to envelope rise/fall bleed, so
			// the thresholds sit below the textbook 1/3/7 dot ratios:
			// a 3-dot character gap can arrive measuring just over two.
			if l <= dot*2 {
				// element gap, nothing
			} else if l <= dot*4 {
				flushSymbol()
			} else {
				flushSymbol()
				sb.WriteByte(' ')
			}
		}
	}
	flushSymbol()
	_ = dots
	stepMs := 1000 * float64(win) / float64(rate)
	result := MorseResult{Text: sb.String(), DotsFound: len(onLens), DotMs: float64(dot) * stepMs,
		ToneHz: bestTone, SnrdB: bestSNR}
	// Noise keys into plausible-looking runs, so the text is only kept when
	// the timing and the character mix both look like real CW.
	if !plausibleMorseRange(result, maxToneHz) {
		result.Text = ""
	}
	return result
}

// plausibleMorse rejects output that is really noise. A CW beacon keys at
// 5-40 words per minute, so a dot lasts 30-200 ms; noise produces runs far
// shorter than that. The character mix must also be mostly decodable.
func plausibleMorse(result MorseResult) bool {
	return plausibleMorseRange(result, 2000)
}

// plausibleMorseRange is plausibleMorse with a caller-set tone ceiling.
// Numbers-station MCW (e.g. the Conet M1 family) keys tones above the
// 2 kHz amateur-beacon range, so the two-tone fallback allows 2.5 kHz.
func plausibleMorseRange(result MorseResult, maxToneHz float64) bool {
	if result.DotMs < 25 || result.DotMs > 250 {
		return false
	}
	if result.ToneHz < 150 || result.ToneHz > maxToneHz {
		return false
	}
	letters, unknown := 0, 0
	for _, r := range result.Text {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			letters++
		case r == '<':
			unknown++
		}
	}
	if letters < 6 {
		return false
	}
	return unknown*3 <= letters
}
