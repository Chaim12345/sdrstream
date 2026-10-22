package decode

import (
	"os"
	"testing"
)

// Live capture from the public Twente WebSDR (websdr.ewi.utwente.nl:8901),
// 7038.6 kHz USB, WSPR window starting 2026-10-22 05:32:00 UTC, resampled to
// 12 kHz. The reference wsprd 1.x (WSJT-X, run with its default and deep
// options) decoded exactly these three spots with the same SNR, DT and
// frequency; the Go port matches them.
func TestWSPRLiveWebSDRWindow(t *testing.T) {
	const f = "testdata/wspr/live_websdr_utwente_7038p6kHz_20261022T0532Z.wav"
	if _, err := os.Stat(f); err != nil {
		t.Skip("live capture fixture absent")
	}
	samples, rate, err := ReadWAV(f)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, m := range WSPRDecode(samples, rate) {
		got[m.Text] = true
	}
	for _, want := range []string{"F4BAD JN25 20", "E79Q JN83 23", "F4WCW JN05 30"} {
		if !got[want] {
			t.Errorf("missing %q, got %v", want, got)
		}
	}
	if len(got) != 3 {
		t.Errorf("expected exactly the 3 reference spots, got %v", got)
	}
}

func TestWSPRNoiseDecodesNothing(t *testing.T) {
	x := make([]float64, 120*12000)
	var s uint32 = 12345
	for i := range x {
		s = s*1664525 + 1013904223
		x[i] = (float64(s>>8)/float64(1<<24) - 0.5) * 0.2
	}
	if got := WSPRDecode(x, 12000); len(got) != 0 {
		t.Fatalf("decoded %v from white noise", got)
	}
}

// Two more live windows with reference wsprd output. 0542: the port matches
// all 6 reference spots. 0544: reference found 7, the port finds 5 of them
// (the two weak ones, EB5RCA and ES1RCG, are missed) and adds nothing else.
func TestWSPRLiveMoreWindows(t *testing.T) {
	cases := map[string][]string{
		"testdata/wspr/live_websdr_utwente_7038p6kHz_20261022T0542Z.wav": {"I0HCJ JN62 27", "E79Q JN83 23", "<...> KO85HX 23", "EB1A IN53 27", "<...> KM56VO 37", "I0LYO JN52 23"},
		"testdata/wspr/live_websdr_utwente_7038p6kHz_20261022T0544Z.wav": {"E79Q JN83 23", "EB5RCA IM98 23", "SV1BHF KM18 23", "I0LYO JN52 23", "ES1RCG KO29 27", "TA4/G8SCU 37", "SP8EB KO11 13"},
	}
	for f, ref := range cases {
		if _, err := os.Stat(f); err != nil {
			t.Skip("fixture absent")
		}
		samples, rate, err := ReadWAV(f)
		if err != nil {
			t.Fatal(err)
		}
		refSet := map[string]bool{}
		for _, r := range ref {
			refSet[r] = true
		}
		hit := 0
		for _, m := range WSPRDecode(samples, rate) {
			if !refSet[m.Text] {
				t.Errorf("%s: spot %q not in reference wsprd output", f, m.Text)
			} else {
				hit++
			}
		}
		if hit < 5 {
			t.Errorf("%s: only %d of %d reference spots", f, hit, len(ref))
		}
	}
}

// M15: a perfectly flat symbol-magnitude window (digital silence) makes
// fac==0, and the un-guarded division produced NaN that failed both clamp
// comparisons and poisoned the Fano input. Pure silence must decode nothing.
func TestWSPRSilenceDecodesNothing(t *testing.T) {
	zeros := make([]float64, 120*12000)
	if got := WSPRDecode(zeros, 12000); len(got) != 0 {
		t.Fatalf("decoded %v from digital silence", got)
	}
}
