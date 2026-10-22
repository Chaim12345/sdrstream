package decode

import "testing"

// realAPRS is 30 s of off-air 144.39 MHz APRS (Los Angeles, 2005) taken from
// Track 1 of WA8LMF's freely distributed "TNC Test CD" (www.wa8lmf.net/TNCtest).
// multimon-ng 1.6.2 AFSK1200 decodes exactly 22 frames from this excerpt and
// every one of them is also decoded here; over the first 5 minutes of the track
// multimon-ng finds 222 frames and this decoder 227, with 217 in common.
const realAPRS = "../../../tests/data/real_aprs_wa8lmf_30s.wav"

func TestAFSKRealOffAir(t *testing.T) {
	packets, err := AFSKFrames(realAPRS)
	if err != nil {
		t.Fatalf("AFSKFrames: %v", err)
	}
	if len(packets) < 22 {
		t.Fatalf("got %d packets, want at least 22 (multimon-ng reference)", len(packets))
	}
	found := false
	for _, p := range packets {
		if p.Source == "KF6DQ-5" {
			found = true
		}
	}
	if !found {
		t.Errorf("station KF6DQ-5 (decoded by multimon-ng) missing")
	}
}
