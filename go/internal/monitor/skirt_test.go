package monitor

import "testing"

func TestSuppressSkirtPeaks(t *testing.T) {
	fm := peak{frequency: 100.800e6, bandwidth: 55_000, levelDB: 27.5}
	edge1 := peak{frequency: 100.7634e6, bandwidth: 0, levelDB: 10.5}
	edge2 := peak{frequency: 100.8415e6, bandwidth: 2_000, levelDB: 10.1}
	// Real-world shape from the 2026-10-25 S23 device logs: two wide FM
	// stations plus a dozen 0-4 kHz edge hits ~10 dB over the floor.
	got := suppressSkirtPeaks([]peak{fm, edge1, edge2})
	if len(got) != 1 || got[0].frequency != fm.frequency {
		t.Fatalf("edge hits survived: %+v", got)
	}
}

func TestSuppressSkirtPeaksKeepsIndependentSignals(t *testing.T) {
	fm := peak{frequency: 100.800e6, bandwidth: 55_000, levelDB: 27.5}
	cases := []struct {
		name string
		p    peak
	}{
		{"narrow far away from the wide signal", peak{frequency: 101.900e6, bandwidth: 3_000, levelDB: 11}},
		{"narrow near a wide but WEAKER signal", peak{frequency: 95.0e6, bandwidth: 2_000, levelDB: 30}},
		{"unknown-width (0) far from wide signals", peak{frequency: 144.8e6, bandwidth: 0, levelDB: 12}},
	}
	for _, tc := range cases {
		got := suppressSkirtPeaks([]peak{fm, tc.p})
		if len(got) != 2 {
			t.Fatalf("%s: wrongly suppressed: %+v", tc.name, got)
		}
	}
}

func TestSuppressSkirtPeaksNeverSuppressesViaNarrowSignals(t *testing.T) {
	// Two narrow signals near each other: neither is wide enough to
	// suppress the other (an SSB pileup must not eat its neighbours).
	a := peak{frequency: 14.074e6, bandwidth: 3_000, levelDB: 25}
	b := peak{frequency: 14.076e6, bandwidth: 2_000, levelDB: 10}
	got := suppressSkirtPeaks([]peak{a, b})
	if len(got) != 2 {
		t.Fatalf("narrow suppressor ate a neighbour: %+v", got)
	}
}

func TestSuppressSkirtPeaksKeepsWideSignalItself(t *testing.T) {
	fm := peak{frequency: 100.800e6, bandwidth: 55_000, levelDB: 27.5}
	edge := peak{frequency: 100.76e6, bandwidth: 1_000, levelDB: 10}
	got := suppressSkirtPeaks([]peak{fm, edge})
	found := false
	for _, p := range got {
		if p.frequency == fm.frequency {
			found = true
		}
	}
	if !found {
		t.Fatalf("the wide signal itself was suppressed: %+v", got)
	}
}
