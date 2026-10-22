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
