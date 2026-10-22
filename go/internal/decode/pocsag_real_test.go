package decode

import (
	"strings"
	"testing"
)

// real_pocsag_1200_taunus.wav is the first 12 s of "POCSAG-1200.WAV" from
// www.signals.taunus.de/WAV/POCSAG.HTML: a real off-air recording of a German
// weather pager network (the text is the transmitter's own, shifted by one
// character in the ASCII table). multimon-ng 1.6.2 POCSAG1200 decodes 13
// pages from this excerpt; the two decoders agree on every one of them.
const realPOCSAG = "../../../tests/data/real_pocsag_1200_taunus.wav"

func TestPOCSAGRealOffAir(t *testing.T) {
	samples, rate, err := ReadWAV(realPOCSAG)
	if err != nil {
		t.Fatal(err)
	}
	msgs := POCSAGDecode(samples, rate, 0)
	if len(msgs) < 13 {
		t.Fatalf("got %d pages, want at least 13 (multimon-ng reference)", len(msgs))
	}
	if msgs[0].Address != 4552 || !strings.HasPrefix(msgs[0].Alpha, "#!ND.Xfuufs!Xbsotubuvtg~s!Ibncvsh") {
		t.Errorf("first page = %+v", msgs[0])
	}
	if !strings.HasPrefix(msgs[2].Alpha, "##Xfjufsf!Sjtjlfo;") {
		t.Errorf("third page = %q", msgs[2].Alpha)
	}
	for _, m := range msgs {
		if m.Alpha == "" && m.Numeric == "" {
			t.Errorf("empty page from noise: %+v", m)
		}
	}
}

func TestPOCSAGSilenceNoPages(t *testing.T) {
	x := make([]float64, 44100)
	if m := POCSAGDecode(x, 22050, 0); len(m) != 0 {
		t.Fatalf("silence decoded %d pages", len(m))
	}
}
