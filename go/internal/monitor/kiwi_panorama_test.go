package monitor

import "testing"

// TestDetectKiwiPanoramaKeepsCentrePeak: a KiwiSDR zoom-0 waterfall spans
// 0-30 MHz and has no server demodulator spike at its centre, so a peak
// at the centre bin (WWV on 15 MHz sits exactly there) must survive.
// The OpenWebRX centre guard exists only for narrow profiles.
func TestDetectKiwiPanoramaKeepsCentrePeak(t *testing.T) {
	frame := make([]float64, 1024)
	for i := range frame {
		frame[i] = -105
	}
	// A strong carrier exactly at the 15 MHz centre of a 0-30 MHz
	// panorama, plus another at ~7.2 MHz.
	for i := 508; i <= 516; i++ {
		frame[i] = -60
	}
	for i := 244; i <= 248; i++ {
		frame[i] = -70
	}
	instance := New(Options{ThresholdDB: 10})
	peaks := instance.detect(frame, 15e6, 30e6)
	if len(peaks) == 0 {
		t.Fatal("panorama peaks not found")
	}
	var centrePeak, lowPeak bool
	for _, p := range peaks {
		if p.frequency > 14.9e6 && p.frequency < 15.1e6 {
			centrePeak = true
		}
		if p.frequency > 7.0e6 && p.frequency < 7.4e6 {
			lowPeak = true
		}
	}
	if !centrePeak {
		t.Error("15 MHz centre peak dropped from a Kiwi panorama; the OpenWebRX demod guard must not apply to wide spans")
	}
	if !lowPeak {
		t.Error("7.2 MHz peak not found")
	}
}

// TestDetectNarrowProfileStillGuardsCentre pins the existing behaviour:
// an OpenWebRX-style narrow profile keeps dropping the centre spike.
func TestDetectNarrowProfileStillGuardsCentre(t *testing.T) {
	frame := make([]float64, 16384)
	for i := range frame {
		frame[i] = -90
	}
	for i := 8180; i <= 8200; i++ {
		frame[i] = -40
	}
	instance := New(Options{ThresholdDB: 10})
	peaks := instance.detect(frame, 7.1e6, 2.048e6)
	for _, p := range peaks {
		if p.frequency > 7.09e6 && p.frequency < 7.11e6 {
			t.Errorf("centre spike at %.0f Hz survived a narrow profile", p.frequency)
		}
	}
}

// TestChooseModeCoarsePanorama: a panorama bin too coarse to measure the
// signal's width must not turn an HF signal into FM.
func TestChooseModeCoarsePanorama(t *testing.T) {
	cases := []struct {
		bandwidth, frequency float64
		want                 string
	}{
		{0, 550e3, "am"},    // MW broadcast
		{0, 1.5e6, "am"},    // top of the MW band
		{0, 7.2e6, "usb"},   // 40 m amateur
		{0, 14.2e6, "usb"},  // 20 m amateur
		{0, 27.5e6, "usb"},  // CB
		{0, 145e6, "fm"},    // VHF stays FM
		{0, 122e6, "am"},    // airband stays AM
		{29e3, 7.2e6, "fm"}, // unchanged: a trusted wide measurement on HF
	}
	for _, c := range cases {
		if got := ChooseModeAt("auto", c.bandwidth, c.frequency); got != c.want {
			t.Errorf("ChooseModeAt(auto, %.0f, %.3e) = %s, want %s", c.bandwidth, c.frequency, got, c.want)
		}
	}
}
