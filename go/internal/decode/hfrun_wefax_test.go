package decode

import (
	"context"
	"testing"
)

// hfrun WEFAX publication semantics, end to end at the decode level:
// a known-good chart is verified, a chart with known-bad geometry is
// not, however real its phasing wedge. The scan publish path drops
// anything whose gate is not Live, so these verdicts are what the
// gallery can ever see.
func TestDecodeHFWEFAXGateSemantics(t *testing.T) {
	target := HFTarget{Kind: "wefax", Name: "probe", LinesPerMin: 120, IOC: 576}
	to16 := func(samples []float64) []int16 {
		out := make([]int16, len(samples))
		for i, v := range samples {
			out[i] = int16(v * 32767)
		}
		return out
	}
	cases := []struct {
		name     string
		fixture  wefaxFixture
		wantLive bool
	}{
		// Charts need 240+ rendered lines to pass the structural
		// image gate, same as real broadcasts. KNOWN OPEN QUESTION:
		// phasing-synced synthetic charts of this length come back
		// with blown tracker wander (drift +3509/+4213 ppm, span
		// ~5000) where the 80-line equivalents track tightly - not
		// yet established whether that is a long-capture tracker
		// limitation or a fixture artifact, so those cases are not
		// pinned here; the gate verdicts below hold either way.
		{"good mid-chart capture", wefaxFixture{rate: 12000, imageLines: 280, bars: true, lineSec: 0.5004, noiseAmp: 0.3}, true},
		{"misaligned flat gradient, real wedge", wefaxFixture{rate: 12000, phasingLines: 60, imageLines: 260, startToneSec: 3, flatGradient: true, noiseAmp: 0.15}, false},
		{"biased mid-chart gray bars", wefaxFixture{rate: 12000, imageLines: 280, bars: true, barGradient: true, gradBands: 4, lineSec: 0.5004, noiseAmp: 0.3}, false},
	}
	for _, tc := range cases {
		samples := encodeWEFAX(tc.fixture)
		res := decodeHF(context.Background(), target, "probe:8073", to16(samples), 12000)
		if res.Gate.Live != tc.wantLive {
			t.Errorf("%s: live=%v want %v (reason %q)", tc.name, res.Gate.Live, tc.wantLive, res.Gate.Reason)
		}
		if len(res.ImagePNG) == 0 {
			t.Errorf("%s: decode produced no image; the unverified-with-report path needs the render", tc.name)
		}
		t.Logf("%s: live=%v reason %q", tc.name, res.Gate.Live, res.Gate.Reason)
	}
}
