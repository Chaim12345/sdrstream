package voice

import (
	"math"
	"strings"
	"testing"
)

// The junk inputs here are real whisper.cpp outputs captured from a
// public WebSDR (music, noise, and fading shortwave), plus the canned
// phrases the model is known to invent.

func TestGuardRejectsMarkerOnlyTranscripts(t *testing.T) {
	for _, text := range []string{
		"[SOUND] [MUSIC]",
		"[MUSIC PLAYING] [MUSIC PLAYING]",
		" [BLANK_AUDIO]",
		"[Music]",
		"[MUSIC]",
		"(applause) [inaudible]",
		"♪ ♪ ♪",
	} {
		v := Guard(text)
		if !v.Rejected {
			t.Errorf("Guard(%q) accepted, want rejection", text)
		}
		if v.Rejected && v.Reason != "markers" {
			t.Errorf("Guard(%q) reason = %q, want markers", text, v.Reason)
		}
	}
}

func TestGuardRejectsCannedHallucinations(t *testing.T) {
	for _, text := range []string{
		"Thank you for watching.",
		"Thanks for watching!",
		"Thank you for watching. Thank you for watching.",
		"Subtitles by the Amara.org community",
		"Please subscribe to my channel.",
		"Bye bye.",
	} {
		v := Guard(text)
		if !v.Rejected {
			t.Errorf("Guard(%q) accepted, want canned rejection", text)
		}
	}
}

func TestGuardRejectsRepetitionLoop(t *testing.T) {
	// Real output from a fading German broadcast on tiny multilingual.
	loop := strings.Repeat("Das ist so super, dass wir das nicht mehr haben. ", 12)
	v := Guard(loop)
	if !v.Rejected || v.Reason != "loop" {
		t.Errorf("Guard(loop) = %+v, want loop rejection", v)
	}
}

func TestGuardRejectsRunOnLoops(t *testing.T) {
	// Real outputs from a fading English broadcast: the loop hides inside
	// one run-on sentence, with no full stops between iterations.
	for _, text := range []string{
		"I think I'm so amazed by saying that I'm so amazed by saying that I'm so amazed by saying that I'm so amazed by that.",
		"I do not know if there is a standard gun I have to do not know if there is a gun I have to do a gun",
	} {
		v := Guard(text)
		if !v.Rejected || v.Reason != "loop" {
			t.Errorf("Guard(%q) = %+v, want loop rejection", text, v)
		}
	}
}

func TestGuardCollapsesShortLoopButKeepsSpeech(t *testing.T) {
	// Two iterations are suspicious but not a loop; the text survives.
	text := "This is the news. This is the news. The president spoke today."
	v := Guard(text)
	if v.Rejected {
		t.Fatalf("Guard(%q) rejected with %q, want accepted", text, v.Reason)
	}
	if !strings.Contains(v.Text, "The president spoke today.") {
		t.Errorf("Guard(%q) text = %q, want the unique sentence kept", text, v.Text)
	}
}

func TestGuardAcceptsRealSpeech(t *testing.T) {
	for _, text := range []string{
		"This is China Radio International. Here is the news. Talks between the two countries continued on Tuesday.",
		"And now the shipping forecast. Viking, North Utsire: southwesterly five to seven.",
		"You're listening to the World Service. The headlines at eight o'clock.",
	} {
		v := Guard(text)
		if v.Rejected {
			t.Errorf("Guard(%q) rejected with %q, want accepted", text, v.Reason)
		}
		if v.Text == "" {
			t.Errorf("Guard(%q) kept no text", text)
		}
	}
}

func TestGuardRejectsTooShort(t *testing.T) {
	for _, text := range []string{
		"Yes.", "Hm.", "A b",
		"One with love here.",                     // music lyric fragment, real capture
		"I'm going to go to the next one.",        // canned outro, real capture
		"And I will say, I will say, I will say.", // short loop, real capture
	} {
		if v := Guard(text); !v.Rejected {
			t.Errorf("Guard(%q) accepted, want short rejection", text)
		}
	}
}

func TestSpeechCoverage(t *testing.T) {
	const rate = 8000
	silence := make([]float64, rate*5)
	if got := SpeechCoverage(silence, rate); got != 0 {
		t.Errorf("coverage of silence = %v, want 0", got)
	}
	// Loud speech-like bursts: amplitude-modulated noise at syllable rate.
	bursts := make([]float64, rate*5)
	seed := uint32(7)
	for i := range bursts {
		seed = seed*1664525 + 1013904223
		noise := float64(int32(seed)) / float64(1<<31)
		envelope := 0.5 + 0.5*math.Sin(2*math.Pi*3*float64(i)/rate)
		bursts[i] = 0.5 * noise * envelope
	}
	if got := SpeechCoverage(bursts, rate); got < 0.3 {
		t.Errorf("coverage of speech-like audio = %v, want > 0.3", got)
	}
}

func TestGuardTranscriptRejectsTextOverSilence(t *testing.T) {
	const rate = 8000
	silence := make([]float64, rate*5)
	v := GuardTranscript(silence, rate, "This is a perfectly plausible sentence of news.")
	if !v.Rejected || v.Reason != "coverage" {
		t.Errorf("GuardTranscript over silence = %+v, want coverage rejection", v)
	}
}

func TestGuardRejectsSmallModelNoiseLabels(t *testing.T) {
	for _, text := range []string{
		"[no speech detected] [no speech detected]", "(machine whirring)",
		"(crickets chirping)", "[beep]", "[ Silence ]", "[BLANK_AUDIO]",
	} {
		if v := Guard(text); !v.Rejected {
			t.Errorf("Guard(%q) accepted: %+v", text, v)
		}
	}
	good := "And so, my fellow Americans, ask not what your country can do for you. Ask what you can do for your country."
	if v := Guard(good); v.Rejected {
		t.Errorf("real speech rejected: %+v", v)
	}
}

func TestSmallQuantisedPreferredOverPlainSmall(t *testing.T) {
	p := []string{"ggml-small.en.bin", "ggml-small.en-q5_1.bin", "ggml-base.en.bin"}
	sortByPreference(p)
	if p[0] != "ggml-base.en.bin" || p[1] != "ggml-small.en-q5_1.bin" {
		t.Errorf("order %v", p)
	}
}
