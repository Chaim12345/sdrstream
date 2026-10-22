package decode

import (
	"math"
	"testing"
	"time"
)

// Reference frames: real ADS-B captures published in the open 1090 MHz
// decoding literature (mode-s.org "The 1090 Megahertz Riddle"). Expected
// values were cross-checked against an independent decoder.

func TestModeSCallsign(t *testing.T) {
	m, err := DecodeModeS("8D4840D6202CC371C32CE0576098")
	if err != nil {
		t.Fatal(err)
	}
	if m.DF != 17 {
		t.Errorf("df = %d, want 17", m.DF)
	}
	if m.ICAO24 != "4840D6" {
		t.Errorf("icao = %s, want 4840D6", m.ICAO24)
	}
	if m.Callsign != "KLM1023" {
		t.Errorf("callsign = %q, want KLM1023", m.Callsign)
	}
}

func TestModeSVelocity(t *testing.T) {
	m, err := DecodeModeS("8D485020994409940838175B284F")
	if err != nil {
		t.Fatal(err)
	}
	if !m.HasVel {
		t.Fatal("no velocity decoded")
	}
	if math.Abs(m.SpeedKt-159.0) > 1.5 {
		t.Errorf("speed = %.1f kt, want ~159", m.SpeedKt)
	}
	if math.Abs(m.TrackDeg-182.88) > 0.5 {
		t.Errorf("track = %.2f, want ~182.88", m.TrackDeg)
	}
	if m.VertRate != -832 {
		t.Errorf("vertical rate = %d fpm, want -832", m.VertRate)
	}
}

func TestModeSPositionCPR(t *testing.T) {
	odd, err := DecodeModeS("8D40621D58C386435CC412692AD6")
	if err != nil {
		t.Fatal(err)
	}
	even, err := DecodeModeS("8D40621D58C382D690C8AC2863A7")
	if err != nil {
		t.Fatal(err)
	}
	if !even.HasAlt || even.Altitude != 38000 {
		t.Errorf("altitude = %d, want 38000", even.Altitude)
	}
	if even.CPROdd {
		t.Error("second frame should be even CPR")
	}
	if !odd.CPROdd {
		t.Error("first frame should be odd CPR")
	}
	lat, lon, err := CPRPosition(even, odd, time.Unix(1457994005, 0), time.Unix(1457994000, 0))
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(lat-52.2572021484375) > 1e-9 {
		t.Errorf("lat = %.10f, want 52.2572021484375", lat)
	}
	if math.Abs(lon-3.91937255859375) > 1e-9 {
		t.Errorf("lon = %.10f, want 3.91937255859375", lon)
	}
}

func TestModeSCRCReject(t *testing.T) {
	if _, err := DecodeModeS("8D4840D6202CC371C32CE0576000"); err == nil {
		t.Fatal("corrupted frame passed CRC")
	}
}

func TestModeSBadInput(t *testing.T) {
	if _, err := DecodeModeS("xyz"); err == nil {
		t.Fatal("non-hex accepted")
	}
	if _, err := DecodeModeS("8D4840"); err == nil {
		t.Fatal("short frame accepted")
	}
}
