package decode

import "testing"

// The FLEX test signals are SYNTHETIC: generated with multimon-ng 1.6.2's
// gen-ng (-f msg -F capcode [-e n]), so they prove interoperability with that
// encoder and the BCH error correction, not behaviour on real off-air audio.
// multimon-ng's FLEX demodulator decodes each of them to the same text.
func TestFlexSynthetic(t *testing.T) {
	cases := []struct {
		file    string
		capcode int64
		text    string
	}{
		{"synth_flex_plain.wav", 1234567, "Hello FLEX pager 123"},
		{"synth_flex_err2.wav", 424242, "Errors: two bits flipped per word, still readable"},
		{"synth_flex_err1.wav", 77, "ALERT: server room temp 41C, call on-call now"},
	}
	for _, c := range cases {
		samples, rate, err := ReadWAV("../../../tests/data/" + c.file)
		if err != nil {
			t.Fatal(err)
		}
		msgs := FlexDecode(samples, rate)
		if len(msgs) != 1 {
			t.Fatalf("%s: got %d pages, want 1: %+v", c.file, len(msgs), msgs)
		}
		if msgs[0].Capcode != c.capcode || msgs[0].Text != c.text {
			t.Errorf("%s: got %d %q, want %d %q", c.file, msgs[0].Capcode, msgs[0].Text, c.capcode, c.text)
		}
	}
}

func TestFlexNoiseNoPages(t *testing.T) {
	x := make([]float64, 44100)
	seed := uint32(1)
	for i := range x {
		seed = seed*1664525 + 1013904223
		x[i] = float64(int32(seed)) / 2e9
	}
	if m := FlexDecode(x, 22050); len(m) != 0 {
		t.Fatalf("noise decoded %d pages", len(m))
	}
}
