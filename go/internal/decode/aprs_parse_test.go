package decode

import (
	"math"
	"testing"
)

// Packets below were decoded off the air from WA8LMF's TNC Test CD (real
// 144.39 MHz traffic) and the expected values were taken from the aprslib
// Python reference parser (0.7.2). Over all 227 frames of the first five
// minutes, 158 of 158 positions aprslib can parse agree with ParseAPRS to
// within 1e-4 degrees.
func TestParseAPRSReal(t *testing.T) {
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-4 }
	cases := []struct {
		name, dest, info string
		lat, lon         float64
		alt              float64
		hasAlt           bool
	}{
		{"mic-e with altitude", "S4QVYV-0", "'.4&l-/k/]\"7q}\r", 34.2826667, -118.4016667, 363, true},
		{"mic-e", "ST0XPR-0", "`-T0m4\x7f>/", 34.1336667, -117.9366667, 0, false},
		{"plain with message capability", "APW275-0", "=3340.25N/11754.88WKPHG2100/WinAPRS 2.7.5 -CAORACOSTA ME-275-<530>\r", 33.6708333, -117.9146667, 0, false},
		{"plain with /A= altitude", "APN383-0", "!3345.71NK11644.26W#PHG5760/PineCove R,Wn,10LNKn/A=006300/www.rivcoraces.org\r", 33.7618333, -116.7376667, 1920.24, true},
		{"weather station", "APRS-0", "!3842.78N/12059.21W_PHG2133/A=000600/ k6ekb@arrl.net", 38.713, -120.9868333, 182.88, true},
		{"object", "APRS-0", ";LA LOAD  *220941z3352.18N\\11749.77W? 114 In 10 Minutes", 33.8696667, -117.8295, 0, false},
	}
	for _, c := range cases {
		r := ParseAPRS(c.dest, c.info)
		if !r.HasPos || !near(r.Lat, c.lat) || !near(r.Lon, c.lon) {
			t.Errorf("%s: got %v,%v (pos=%v) want %v,%v", c.name, r.Lat, r.Lon, r.HasPos, c.lat, c.lon)
		}
		if r.HasAlt != c.hasAlt || (c.hasAlt && math.Abs(r.Altitude-c.alt) > 1) {
			t.Errorf("%s: altitude %v (has=%v) want %v", c.name, r.Altitude, r.HasAlt, c.alt)
		}
	}
	m := ParseAPRS("APK101-0", ":K6XTY    : o3{P\r")
	if m.Kind != "message" || m.To != "K6XTY" || m.Text != "o3" || m.MsgID != "P" {
		t.Errorf("message: %+v", m)
	}
}

func TestParseAPRSCompressed(t *testing.T) {
	// Example from the APRS 1.0.1 spec, section 9: /5L!!<*e7> sT
	r := ParseAPRS("APRS", "=/5L!!<*e7>7P[")
	if !r.HasPos || math.Abs(r.Lat-49.5) > 0.01 || math.Abs(r.Lon+72.75) > 0.01 {
		t.Errorf("compressed: %+v", r)
	}
}

func TestParseAPRSNMEA(t *testing.T) {
	r := ParseAPRS("GPSLJ-0", "$GPRMC,013641.06,A,3348.1607,N,11807.4631,W,34.0,090.5,231105,13.,E*73\r")
	if !r.HasPos || math.Abs(r.Lat-33.802678) > 1e-5 || math.Abs(r.Lon+118.124385) > 1e-5 {
		t.Errorf("rmc: %+v", r)
	}
}

func TestAFSKRealOffAirParsed(t *testing.T) {
	packets, err := AFSKFrames(realAPRS)
	if err != nil {
		t.Fatal(err)
	}
	pos := 0
	for _, p := range packets {
		if r := p.APRS(); r.HasPos && r.Lat > 33 && r.Lat < 35 && r.Lon < -117 && r.Lon > -119 {
			pos++
		}
	}
	if pos < 10 {
		t.Errorf("only %d of %d real packets parsed to a Los Angeles position", pos, len(packets))
	}
}
