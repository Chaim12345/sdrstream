package decode

import (
	"math"
	"strings"
	"testing"
	"time"
)

// Characterization of current behaviour on committed reference fixtures
// (ref_psk31.wav / ref_dtmf.wav are synthetic generator output, not RF)
// plus hostile inputs. No production code is modified.

func TestQAPSK31AutoSearchAndGuards(t *testing.T) {
	samples, rate, err := ReadWAV(refPSK31)
	if err != nil {
		t.Fatal(err)
	}
	// centerHz 0 = search the 600-1400 Hz window; must still find the text.
	if got := PSK31Decode(samples, rate, 0); !strings.Contains(got, "CQ DE RS44 PSK31 TEST") {
		t.Fatalf("auto-search = %q", got)
	}
	// Far from the carrier the decode must not invent the sent text.
	if got := PSK31Decode(samples, rate, 1800); strings.Contains(got, "RS44") {
		t.Fatalf("off-carrier decode produced the real text: %q", got)
	}
	if PSK31Decode(samples, 7999, 1000) != "" {
		t.Fatal("rate<8000 must return empty")
	}
	if PSK31Decode(samples[:rate/4-1], rate, 1000) != "" {
		t.Fatal("under 250 ms must return empty")
	}
	if PSK31Decode(nil, rate, 1000) != "" {
		t.Fatal("nil must return empty")
	}
	if got := PSK31Decode(qaNoise(rate, 4, 0.3), rate, 0); got != "" {
		t.Logf("white noise decoded to %q (characterization: gate score>=4 let this through)", got)
	}
}

func TestQADTMFGuardsAndNoise(t *testing.T) {
	const rate = 8000
	if got := DTMFDecode(nil, rate); got != "" {
		t.Fatalf("nil: %q", got)
	}
	if got := DTMFDecode(make([]float64, rate), rate); got != "" {
		t.Fatalf("silence: %q", got)
	}
	if got := DTMFDecode(qaTone(1000, rate, 1, 0.5), rate); got != "" {
		t.Fatalf("single tone is not a digit: %q", got)
	}
	// Design rule: fewer than 3 digits is never reported. One digit -> "".
	dig := func(row, col float64) []float64 {
		x, y := qaTone(row, rate, 0.1, 0.3), qaTone(col, rate, 0.1, 0.3)
		out := make([]float64, 0, rate/5)
		for i := range x {
			out = append(out, x[i]+y[i])
		}
		return append(out, make([]float64, rate/20)...)
	}
	one := dig(697, 1209)
	if got := DTMFDecode(append(make([]float64, rate/10), one...), rate); got != "" {
		t.Fatalf("a single digit must not be reported (min 3), got %q", got)
	}
	seq := make([]float64, rate/10)
	seq = append(seq, dig(697, 1209)...) // 1
	seq = append(seq, dig(770, 1336)...) // 5
	seq = append(seq, dig(852, 1477)...) // 9
	if got := DTMFDecode(seq, rate); got != "159" {
		t.Fatalf("synthetic 1,5,9 decoded as %q", got)
	}
	if got := DTMFDecode(qaNoise(rate, 2, 0.3), rate); len(got) > 3 {
		t.Fatalf("2 s of white noise produced %d digits: %q", len(got), got)
	}
}

func TestQAHostileSamplesDoNotPanic(t *testing.T) {
	const rate = 8000
	bad := map[string][]float64{
		"nan":  {math.NaN(), math.NaN(), math.NaN(), math.NaN()},
		"inf":  {math.Inf(1), math.Inf(-1), math.Inf(1), math.Inf(-1)},
		"huge": {1e300, -1e300, 1e300, -1e300},
	}
	for name, base := range bad {
		s := make([]float64, 0, rate)
		for len(s) < rate {
			s = append(s, base...)
		}
		t.Run(name, func(t *testing.T) {
			_ = Classify(s, rate)
			if name != "huge" { // huge-magnitude powers have a dedicated timeout test below
				_ = OccupiedBandwidth(s, rate)
			}
			_ = DTMFDecode(s, rate)
			_ = PSK31Decode(s, rate, 1000)
			_, _, _ = morseFromSamples(s, rate)
			_ = morseEnvelope(s, rate, 700)
		})
	}
	for _, r := range []int{-1, 0, 1, 100} {
		s := qaTone(500, 8000, 0.5, 0.5)
		t.Run("rate", func(t *testing.T) {
			_ = Classify(s, r)
			_ = OccupiedBandwidth(s, r)
			_ = DTMFDecode(s, r)
			_ = PSK31Decode(s, r, 1000)
		})
	}
}

// Hardening regression (fixed in 727aacf; low reachability: ReadWAV yields
// only samples in [-1,1]): OccupiedBandwidth must return for +-1e300
// samples even when powers overflow to Inf/NaN. Fails with a timeout
// after 3 s if the grow loop ever loses its progress guarantee.
func TestQAOccupiedBandwidthHugeSamplesDefect(t *testing.T) {
	s := make([]float64, 8000)
	for i := range s {
		s[i] = 1e300 * math.Pow(-1, float64(i))
	}
	done := make(chan struct{})
	go func() { _ = OccupiedBandwidth(s, 8000); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("OccupiedBandwidth did not return within 3 s for huge-magnitude samples")
	}
}
