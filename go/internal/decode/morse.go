package decode

import (
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
	samples, rate, err := ReadWAV(path)
	if err != nil {
		return MorseResult{}, err
	}
	res, tone, snr := morseFromSamples(samples, rate)
	res.ToneHz, res.SnrdB = tone, snr
	return res, nil
}

func morseFromSamples(samples []float64, rate int) (MorseResult, float64, float64) {
	// Analyse in 1 s windows: pick the strongest window's tone once, then
	// track the envelope at that frequency for the whole file.
	chunk := rate
	if len(samples) < chunk {
		chunk = len(samples)
	}
	bestTone, bestSNR := 0.0, math.Inf(-1)
	for start := 0; start+chunk <= len(samples); start += chunk {
		f, snr := FindDominantTone(samples[start:start+chunk], rate)
		if snr > bestSNR {
			bestTone, bestSNR = f, snr
		}
	}
	if bestTone <= 0 {
		return MorseResult{Text: ""}, 0, bestSNR
	}

	// Envelope at the tone frequency: 10 ms Goertzel magnitude windows.
	win := rate / 100
	if win < 1 {
		win = 1
	}
	var env []float64
	for start := 0; start+win <= len(samples); start += win {
		env = append(env, goertzel(samples[start:start+win], rate, bestTone))
	}
	if len(env) < 3 {
		return MorseResult{Text: ""}, bestTone, bestSNR
	}
	sort2 := make([]float64, len(env))
	copy(sort2, env)
	sort.Float64s(sort2)
	floor := sort2[len(sort2)/4] // noise floor from the quietest quartile
	peak := sort2[3*len(sort2)/4]
	if peak <= floor {
		return MorseResult{Text: ""}, bestTone, bestSNR
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
		return MorseResult{Text: ""}, bestTone, bestSNR
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
			// Gap: intra-character, inter-character or word.
			if l <= dot {
				// element gap, nothing
			} else if l <= dot*3+1 {
				flushSymbol()
			} else if l >= dot*5 {
				flushSymbol()
				sb.WriteByte(' ')
			}
		}
	}
	flushSymbol()
	_ = dots
	return MorseResult{Text: sb.String(), DotsFound: len(onLens)}, bestTone, bestSNR
}
