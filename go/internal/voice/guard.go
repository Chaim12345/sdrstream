package voice

// guard.go is the whisper quality gate: whisper.cpp invents text when it
// hears music, noise or silence, and shortwave audio gives it plenty of
// all three. Nothing the recogniser says may reach a user-visible feed
// until it survives the checks here. The checks are deliberately simple
// and conservative: they only ever remove text, they never "fix" it.
//
// Three failure shapes are caught:
//
//  1. Non-speech markers: whisper labels music and noise with bracketed
//     tokens ([music], (applause), [BLANK_AUDIO] ...). A transcript that
//     is only markers contains no words at all.
//  2. Canned hallucinations: silence and jingles reliably produce the
//     same YouTube-flavoured phrases ("thank you for watching",
//     "subtitles by ..."). They are never broadcast content.
//  3. Repetition loops: on fading or noisy speech the decoder repeats
//     one invented sentence for the whole clip. Real speech does not
//     say the same sentence six times in a row.

import (
	"strings"
	"unicode"
)

// Verdict is the guard's decision about one transcript.
type Verdict struct {
	// Text is the text worth keeping: markers stripped and duplicate
	// loop iterations collapsed. Empty when Rejected is true.
	Text string
	// Rejected is true when the transcript failed the gate.
	Rejected bool
	// Reason names the failed check ("markers", "canned", "loop",
	// "empty", "short"); empty when accepted.
	Reason string
}

// nonSpeechMarkers are the bracketed tokens whisper.cpp emits for audio
// that is not speech. Matching is case-insensitive on the inner text.
var nonSpeechMarkers = []string{
	"music", "music playing", "sound", "silence", "blank_audio",
	"blank audio", "applause", "laughter", "laughing", "inaudible",
	"noise", "static", "beep", "beeps", "ringing", "static crackling",
	"indistinct chatter", "foreign", "speaking foreign language",
	"crowd noise", "wind", "static noise", "no audio",
}

// cannedHallucinations are whole-phrase hallucinations whisper.cpp is
// known to produce for silence, noise beds and music: the vocabulary of
// its YouTube-heavy training data, not of broadcast radio.
var cannedHallucinations = []string{
	"thank you for watching", "thanks for watching",
	"thank you for listening", "thanks for listening",
	"thank you so much for watching", "thank you for your watching",
	"please subscribe", "subscribe to my channel", "like and subscribe",
	"don't forget to subscribe", "subtitles by", "subtitle by",
	"captions by", "captioned by", "translated by", "translation by",
	"all rights reserved", "copyright", "see you in the next video",
	"see you next time", "i'll see you in the next one",
	"watching this video", "hope you enjoyed", "see you guys",
	"bye bye", "you're watching", "stay tuned for more videos",
	"going to go to the next one", "go to the next one",
	"i'm going to go to the next one",
}

// markerToken reports whether a bracketed token like "[music]" or
// "( applause )" is a non-speech marker, and returns its length.
func markerToken(text string) (int, bool) {
	if len(text) < 3 {
		return 0, false
	}
	open, close := text[0], byte(0)
	switch open {
	case '[':
		close = ']'
	case '(':
		close = ')'
	default:
		return 0, false
	}
	end := strings.IndexByte(text, close)
	if end < 2 {
		return 0, false
	}
	inner := strings.TrimSpace(text[1:end])
	inner = strings.ToLower(inner)
	inner = strings.Trim(inner, ".! ")
	for _, marker := range nonSpeechMarkers {
		if inner == marker {
			return end + 1, true
		}
	}
	return 0, false
}

// stripMarkers removes bracketed non-speech markers and music symbols.
func stripMarkers(text string) string {
	var out strings.Builder
	for i := 0; i < len(text); {
		if n, ok := markerToken(text[i:]); ok {
			i += n
			continue
		}
		r := rune(text[i])
		if r == 0xE2 && i+2 < len(text) { // multi-byte music notes ♪ ♫
			out.WriteByte(text[i])
			i++
			continue
		}
		out.WriteByte(text[i])
		i++
	}
	cleaned := out.String()
	for _, note := range []string{"♪", "♫", "♬", "♩"} {
		cleaned = strings.ReplaceAll(cleaned, note, " ")
	}
	return strings.Join(strings.Fields(cleaned), " ")
}

// letters counts the letters in text.
func letters(text string) int {
	n := 0
	for _, r := range text {
		if unicode.IsLetter(r) {
			n++
		}
	}
	return n
}

// normaliseSentence folds case and punctuation so two loop iterations of
// the same hallucinated sentence compare equal.
func normaliseSentence(s string) string {
	var out strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == ' ' {
			out.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(out.String()), " ")
}

// splitSentences splits on sentence-ending punctuation and newlines;
// whisper's plain-text output separates loop iterations with ". ".
func splitSentences(text string) []string {
	var parts []string
	var current strings.Builder
	for _, r := range text {
		current.WriteRune(r)
		if r == '.' || r == '!' || r == '?' || r == '\n' {
			if s := strings.TrimSpace(current.String()); s != "" {
				parts = append(parts, s)
			}
			current.Reset()
		}
	}
	if s := strings.TrimSpace(current.String()); s != "" {
		parts = append(parts, s)
	}
	return parts
}

// wordCount counts words without allocating.
func wordCount(text string) int { return len(strings.Fields(text)) }

// isCanned reports whether the normalised text is a canned hallucination:
// it either starts with a known phrase (the tail of such lines is part of
// the same invention: "subtitles by the ... community") or is made of the
// phrases and nothing else.
func isCanned(text string) bool {
	rest := normaliseSentence(text)
	if rest == "" {
		return false
	}
	for _, phrase := range cannedHallucinations {
		if strings.HasPrefix(rest, normaliseSentence(phrase)) {
			return true
		}
	}
	for {
		matched := false
		for _, phrase := range cannedHallucinations {
			p := normaliseSentence(phrase)
			if strings.HasPrefix(rest, p) {
				rest = strings.TrimSpace(strings.TrimPrefix(rest, p))
				matched = true
				break
			}
		}
		if !matched {
			break
		}
	}
	// "thank you for watching thank you for watching" reduces to "".
	return rest == ""
}

// collapseLoop detects a repeated-sentence loop. It returns the text with
// duplicate loop iterations removed, the share of words belonging to the
// most repeated sentence, and how often it occurred.
func collapseLoop(text string) (string, float64, int) {
	sentences := splitSentences(text)
	if len(sentences) < 2 {
		return text, 0, 0
	}
	counts := map[string]int{}
	words := map[string]int{}
	total := 0
	for _, s := range sentences {
		key := normaliseSentence(s)
		if key == "" {
			continue
		}
		counts[key]++
		w := wordCount(s)
		words[key] = w
		total += w
	}
	top, topCount := "", 0
	for key, n := range counts {
		if n > topCount {
			top, topCount = key, n
		}
	}
	if total == 0 {
		return text, 0, 0
	}
	share := float64(words[top]*topCount) / float64(total)
	if topCount < 2 {
		return text, share, topCount
	}
	// Keep one copy of each sentence, in first-appearance order, to
	// salvage anything real said around the loop.
	seen := map[string]bool{}
	var kept []string
	for _, s := range sentences {
		key := normaliseSentence(s)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		kept = append(kept, s)
	}
	return strings.Join(kept, " "), share, topCount
}

// ngramLoop finds a repeated word n-gram that dominates the text. Loops
// do not always fall on sentence boundaries: on a fading signal whisper
// emits one long sentence with the same six words over and over. Returns
// the largest share of words covered by any phrase repeated enough times.
func ngramLoop(words []string) float64 {
	if len(words) < 8 {
		return 0
	}
	norm := make([]string, len(words))
	for i, w := range words {
		norm[i] = normaliseSentence(w)
	}
	best := 0.0
	for n := 8; n >= 4; n-- {
		if len(norm) < n*2 {
			continue
		}
		counts := map[string]int{}
		for i := 0; i+n <= len(norm); i++ {
			counts[strings.Join(norm[i:i+n], " ")]++
		}
		top := 0
		for _, c := range counts {
			if c > top {
				top = c
			}
		}
		if top < 2 {
			continue
		}
		// Windows overlap, so count*length overstates the coverage of a
		// tight loop; halving it is a fair estimate and still catches one.
		coverage := float64(top*n) / float64(len(norm)) / 2
		if coverage > best {
			best = coverage
		}
	}
	return best
}

// Guard decides whether a whisper transcript may be shown to a user.
// It never invents content: accepted text is a subset of what whisper
// produced, rejected text is dropped entirely.
func Guard(text string) Verdict {
	cleaned := stripMarkers(text)
	if cleaned == "" {
		return Verdict{Rejected: true, Reason: "markers"}
	}
	if isCanned(cleaned) {
		return Verdict{Rejected: true, Reason: "canned"}
	}
	collapsed, share, repeats := collapseLoop(cleaned)
	// One sentence said three or more times, making up at least half the
	// words, is a decoder loop, not speech. The same goes for a repeated
	// phrase inside a single run-on sentence.
	if repeats >= 3 && share >= 0.5 {
		return Verdict{Rejected: true, Reason: "loop"}
	}
	if ngramLoop(strings.Fields(collapsed)) >= 0.25 {
		return Verdict{Rejected: true, Reason: "loop"}
	}
	// A broadcast sentence that survives everything else still has to say
	// something: lyric fragments of five words are a hallucination shape
	// too, and real news speech never arrives in five-word clips.
	if letters(collapsed) < 20 || wordCount(collapsed) < 6 {
		return Verdict{Rejected: true, Reason: "short"}
	}
	return Verdict{Text: collapsed}
}

// SpeechCoverage reports the fraction of a clip that carries speech-like
// energy, using the same activity detector as the monitor. A long
// transcript over a mostly-silent clip is a hallucination even when the
// words look plausible.
func SpeechCoverage(samples []float64, rate int) float64 {
	if len(samples) == 0 || rate <= 0 {
		return 0
	}
	var covered float64
	for _, segment := range SpeechActivity(samples, rate) {
		covered += segment.End - segment.Start
	}
	coverage := covered / (float64(len(samples)) / float64(rate))
	if coverage > 1 {
		coverage = 1
	}
	return coverage
}

// GuardTranscript combines the text gate with the audio cross-check: a
// transcript whose words far outrun the measured speech in the clip is
// rejected as "coverage".
func GuardTranscript(samples []float64, rate int, text string) Verdict {
	verdict := Guard(text)
	if verdict.Rejected {
		return verdict
	}
	if len(samples) > 0 && SpeechCoverage(samples, rate) < 0.05 {
		return Verdict{Rejected: true, Reason: "coverage"}
	}
	return verdict
}
