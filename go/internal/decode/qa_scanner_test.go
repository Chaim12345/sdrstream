package decode

import (
	"errors"
	"math"
	"os"
	"strings"
	"testing"
)

// Synthetic signals only (generated in-process, deterministic). These pin
// the CURRENT behaviour of the survey classifier; they are not real-RF proof.

func qaTone(hz float64, rate int, secs, amp float64) []float64 {
	n := int(float64(rate) * secs)
	out := make([]float64, n)
	for i := range out {
		out[i] = amp * math.Sin(2*math.Pi*hz*float64(i)/float64(rate))
	}
	return out
}

func qaNoise(rate int, secs, amp float64) []float64 {
	n := int(float64(rate) * secs)
	out := make([]float64, n)
	x := uint32(12345)
	for i := range out {
		x = x*1664525 + 1013904223
		out[i] = amp * (float64(x>>8)/float64(1<<24)*2 - 1)
	}
	return out
}

func qaKeyed(hz float64, rate int, amp float64) []float64 {
	var out []float64
	for k := 0; k < 12; k++ {
		out = append(out, qaTone(hz, rate, 0.1, amp)...)
		out = append(out, make([]float64, rate/10)...)
	}
	return out
}

func TestQAClassifyKinds(t *testing.T) {
	const rate = 8000
	if h := Classify(nil, rate); h.Kind != "silence" || h.LevelDB != -100 {
		t.Fatalf("empty: %+v", h)
	}
	if h := Classify(make([]float64, rate), rate); h.Kind != "silence" {
		t.Fatalf("zeros: %+v", h)
	}
	if h := Classify(qaTone(1000, rate, 1, 0.005), rate); h.Kind != "silence" {
		t.Fatalf("peak<0.02 must be silence: %+v", h)
	}
	if h := Classify(qaNoise(rate, 1, 0.3), rate); h.Kind != "noise" {
		t.Fatalf("white noise: %+v", h)
	}
	steady := Classify(qaTone(1000, rate, 1, 0.5), rate)
	if steady.Kind != "data" {
		t.Fatalf("steady carrier is classified data (not cw): %+v", steady)
	}
	if math.Abs(steady.ToneHz-1000) > 100 {
		t.Fatalf("tone %.0f not near 1000", steady.ToneHz)
	}
	if keyed := Classify(qaKeyed(1000, rate, 0.5), rate); keyed.Kind != "cw" && keyed.Kind != "data" {
		t.Fatalf("keyed tone: %+v", keyed)
	} else {
		t.Logf("keyed tone classified %q (characterization)", keyed.Kind)
	}
}

func TestQAScanRange(t *testing.T) {
	const rate = 8000
	var tuned []float64
	s := &BandScanner{StepHz: 1000}
	s.Tune = func(hz float64, mode string) error {
		if hz == 2000 {
			return errors.New("tune refused")
		}
		tuned = append(tuned, hz)
		return nil
	}
	s.Capture = func() ([]float64, int, error) {
		switch len(tuned) {
		case 1:
			return qaTone(1000, rate, 1, 0.5), rate, nil // data
		case 2:
			return make([]float64, rate), rate, nil // silence, dropped
		case 3:
			return qaTone(1000, rate, 0.05, 0.5), rate, nil // too short (<rate/10), skipped
		default:
			return nil, 0, errors.New("capture failed")
		}
	}
	hits := s.ScanRange(1000, 5000, 0, nil) // stepHz 0 falls back to s.StepHz, nil modes -> fm
	if len(hits) != 1 {
		t.Fatalf("want exactly 1 hit (silence, short and failed captures dropped), got %+v", hits)
	}
	h := hits[0]
	if h.FrequencyHz != 1000 || h.Mode != "fm" || h.Kind == "silence" {
		t.Fatalf("hit: %+v", h)
	}
	for _, f := range tuned {
		if f == 2000 {
			t.Fatal("refused frequency recorded as tuned")
		}
	}
	if len(tuned) != 4 { // 1000,3000,4000,5000 ; 2000 refused
		t.Fatalf("tuned %v", tuned)
	}
}

func TestQAScanRangeModesAndEmptyRange(t *testing.T) {
	calls := 0
	s := &BandScanner{StepHz: 500,
		Tune:    func(float64, string) error { calls++; return nil },
		Capture: func() ([]float64, int, error) { return nil, 8000, nil }}
	if hits := s.ScanRange(10, 5, 100, []string{"am", "usb"}); len(hits) != 0 || calls != 0 {
		t.Fatalf("end<start must do nothing: hits=%v calls=%d", hits, calls)
	}
	s.ScanRange(1000, 1000, 100, []string{"am", "usb"})
	if calls != 2 {
		t.Fatalf("one frequency x two modes = 2 tunes, got %d", calls)
	}
}

func TestQAOccupiedBandwidth(t *testing.T) {
	const rate = 8000
	if OccupiedBandwidth(nil, rate) != 0 || OccupiedBandwidth(make([]float64, 100), rate) != 0 {
		t.Fatal("short input must be 0")
	}
	if OccupiedBandwidth(qaTone(1000, rate, 1, 0.5), 0) != 0 {
		t.Fatal("rate<=0 must be 0")
	}
	if OccupiedBandwidth(make([]float64, rate), rate) != 0 {
		t.Fatal("silence must be 0")
	}
	// Noise fills the whole 300-2700 Hz passband: the width must stay inside it.
	wide := OccupiedBandwidth(qaNoise(rate, 1, 0.5), rate)
	if wide < 1500 || wide > 2400 {
		t.Fatalf("noise width %.0f Hz outside the 300-2700 Hz passband search", wide)
	}
}

// Regression for the defect QA found (fixed in 727aacf): for a narrow
// signal OccupiedBandwidth returned absurd widths (1 kHz tone: ~1,370,798
// Hz; 500 Hz tone: ~6.8 MHz; 2 kHz tone: 19 Hz). The documented contract
// is a width inside the 300-2700 Hz search band.
func TestQAOccupiedBandwidthNarrowSignalDefect(t *testing.T) {
	const rate = 8000
	for _, hz := range []float64{500, 1000, 1500, 2000} {
		w := OccupiedBandwidth(qaTone(hz, rate, 1, 0.5), rate)
		if w <= 0 || w > 400 {
			t.Errorf("%.0f Hz tone: width %.0f Hz, want a narrow width within the passband", hz, w)
		}
	}
}

func TestQAReportOrdersStrongestFirstAndDoesNotMutate(t *testing.T) {
	in := []BandHit{
		{FrequencyHz: 7.0e6, Mode: "usb", Kind: "voice", LevelDB: -30},
		{FrequencyHz: 14.0e6, Mode: "cw", Kind: "cw", LevelDB: -10},
	}
	out := Report(in)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "14.0000 MHz") || !strings.Contains(lines[1], "7.0000 MHz") {
		t.Fatalf("order: %q", out)
	}
	if in[0].FrequencyHz != 7.0e6 {
		t.Fatal("Report mutated its input")
	}
	if Report(nil) != "" {
		t.Fatal("empty report must be empty string")
	}
}

func TestQAMorseHelpers(t *testing.T) {
	if n := morseLetters("CQ 73 de k1abc ?/"); n != 5 { // C Q 7 3 1 ; lowercase and punctuation are not counted
		t.Fatalf("morseLetters=%d", n)
	}
	const rate = 8000
	env := morseEnvelope(qaKeyed(700, rate, 0.5), rate, 700)
	if len(env) != 240 { // 12 * 0.2 s / 10 ms
		t.Fatalf("envelope windows=%d", len(env))
	}
	on, off := env[0], env[15]
	if !(on > 10*off) {
		t.Fatalf("keyed on (%g) should dwarf off (%g)", on, off)
	}
	if got := morseEnvelope(make([]float64, 5), 8000, 700); len(got) != 0 {
		t.Fatalf("less than one window should give no envelope, got %d", len(got))
	}
}

func TestQACreateTempWAV(t *testing.T) {
	f, err := createTempWAV()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if !strings.HasSuffix(f.Name(), ".wav") || !strings.Contains(f.Name(), "sdrstream-hf-") {
		t.Fatalf("name %q", f.Name())
	}
}

// KNOWN SEMANTIC GAP (found on integration 4d7f46a): the grow-outward search is
// greedy and contiguous, so a two-peak signal overshoots. Two simultaneous
// equal tones 170 Hz apart (RTTY-like, synthetic) measure ~1424 Hz, while the
// narrowest interval holding 90% of the passband power is ~206 Hz (computed
// by brute force below). Single tones and noise are fine. QA_KNOWN_DEFECTS=1.
func TestQAOccupiedBandwidthTwoTonesGap(t *testing.T) {
	const rate = 8000
	a, b := qaTone(1000, rate, 1, 0.3), qaTone(1170, rate, 1, 0.3)
	for i := range a {
		a[i] += b[i]
	}
	got := OccupiedBandwidth(a, rate)
	// Regression for the greedy-expansion defect fixed in f881646: the
	// narrowest 90% interval here is ~206 Hz; following the sidelobes
	// outward from the peak measured ~1.4 kHz.
	if got <= 0 || got > 500 {
		t.Fatalf("two tones 170 Hz apart measured %.0f Hz; the narrowest 90%% interval is ~206 Hz", got)
	}
}
