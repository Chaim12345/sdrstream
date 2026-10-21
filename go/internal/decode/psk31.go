package decode

import (
	"math"
	"sort"
	"strings"
)

// PSK31 is a narrowband HF keyboard mode: differential BPSK at 31.25 baud
// with raised-cosine shaping, carrying Varicode text. The signal sits at an
// audio offset chosen by the operator, most often near 1000 Hz. Decoding
// mixes the audio down to baseband, integrates one symbol at a time, and
// takes the sign of the dot product between consecutive symbols to recover
// each differentially-encoded bit.
func PSK31Decode(samples []float64, rate int, centerHz float64) string {
	if rate < 8000 || len(samples) < rate/4 {
		return ""
	}
	candidates := []float64{centerHz}
	if centerHz == 0 {
		// No offset given: search the usual PSK31 audio window.
		candidates = nil
		for f := 600.0; f <= 1400.0; f += 25 {
			candidates = append(candidates, f)
		}
	}
	best, bestScore := "", -1
	for _, f := range candidates {
		text := psk31At(samples, rate, f)
		if score := printableScore(text); score > bestScore {
			best, bestScore = text, score
		}
	}
	if bestScore < 4 {
		return ""
	}
	return best
}

// psk31At decodes assuming a carrier at centerHz and returns the text.
func psk31At(samples []float64, rate int, centerHz float64) string {
	spb := float64(rate) / 31.25
	symbols := int(float64(len(samples)) / spb)
	if symbols < 8 {
		return ""
	}
	// Per-symbol matched filter: correlate the window with cos/sin at the
	// carrier to get baseband I and Q.
	is := make([]float64, symbols)
	qs := make([]float64, symbols)
	for k := 0; k < symbols; k++ {
		start := int(float64(k) * spb)
		end := int(float64(k+1) * spb)
		if end > len(samples) {
			end = len(samples)
		}
		var re, im float64
		for t := start; t < end; t++ {
			ph := 2 * math.Pi * centerHz * float64(t) / float64(rate)
			re += samples[t] * math.Cos(ph)
			im -= samples[t] * math.Sin(ph)
		}
		is[k], qs[k] = re, im
	}
	// Differential detection: a phase reversal (bit 0) makes the dot product
	// of consecutive symbols negative; no reversal (bit 1) keeps it positive.
	//
	// The normalised dot product also says how clean the signal is. In a
	// real PSK31 transmission every symbol sits at the same phase, so the
	// correlation between neighbouring symbols is close to one; noise
	// scatters the phase and the correlation collapses. That measurement
	// keeps noise from being decoded into plausible-looking text.
	correlations := make([]float64, 0, symbols)
	var bits strings.Builder
	for k := 1; k < symbols; k++ {
		dot := is[k]*is[k-1] + qs[k]*qs[k-1]
		magnitude := math.Hypot(is[k], qs[k]) * math.Hypot(is[k-1], qs[k-1])
		if magnitude > 0 {
			correlations = append(correlations, math.Abs(dot/magnitude))
		}
		if dot >= 0 {
			bits.WriteByte('1')
		} else {
			bits.WriteByte('0')
		}
	}
	if len(correlations) < 8 || medianAbs(correlations) < 0.9 {
		return ""
	}
	raw := bits.String()
	// Differential detection loses the very first symbol, so the recovered
	// stream may be shifted by one bit relative to the true characters. Try
	// both alignments (and both polarities) and keep the most text-like.
	best, bestScore := "", -1
	for _, candidate := range []string{
		raw,
		invertBits(raw),
		"1" + raw,
		invertBits("1" + raw),
	} {
		// A real PSK31 signal only ever contains varicode codes, so a
		// stream with invalid segments is noise, however text-like the
		// characters that did come out look.
		text, invalid, idles := decodeVaricodeStrict(candidate)
		if invalid > 0 || idles == 0 {
			continue
		}
		if s := printableScore(text); s > bestScore {
			best, bestScore = text, s
		}
	}
	return best
}

func invertBits(bits string) string {
	out := []byte(bits)
	for i := range out {
		if out[i] == '0' {
			out[i] = '1'
		} else {
			out[i] = '0'
		}
	}
	return string(out)
}

// printableScore rates how much a decoded string looks like real text, used
// to pick the best frequency offset, bit polarity and bit alignment. Besides
// rewarding letters and digits it penalises control characters and stray
// single-letter "words", which random bit patterns produce far more often
// than real text does.
func printableScore(s string) int {
	score := 0
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			score += 2
		case r == ' ' || r == '\n' || r == '\r':
			score++
		case r >= '!' && r <= '~':
			score++
		default:
			score -= 4 // control or non-ASCII: strongly suspect
		}
	}
	for _, word := range strings.FieldsFunc(s, func(r rune) bool {
		return r == ' ' || r == '\n' || r == '\r' || r == '\t'
	}) {
		if len([]rune(word)) == 1 && !strings.ContainsAny(word, "aAiI") {
			score -= 3
		}
		// Real text is consistently cased; random bit patterns mix cases
		// inside a single word, and sprinkle punctuation through words.
		hasUpper := strings.IndexFunc(word, func(r rune) bool { return r >= 'A' && r <= 'Z' }) >= 0
		hasLower := strings.IndexFunc(word, func(r rune) bool { return r >= 'a' && r <= 'z' }) >= 0
		if hasUpper && hasLower {
			score -= 4
		}
	}
	return score
}

// medianAbs returns the middle value of the absolute values, which is
// used to judge how cleanly a phase-modulated signal is locked.
func medianAbs(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	return sorted[len(sorted)/2]
}
