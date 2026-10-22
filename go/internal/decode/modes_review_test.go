package decode

import (
	"encoding/hex"
	"strings"
	"testing"
)

// buildReviewDF17 assembles a DF17 extended squitter with a valid CRC-24
// parity so probes can set individual ME fields.
func buildReviewDF17(icao uint32, me [7]byte) string {
	msg := make([]byte, 14)
	msg[0] = 17<<3 | 5 // DF=17, CA=5
	msg[1] = byte(icao >> 16)
	msg[2] = byte(icao >> 8)
	msg[3] = byte(icao)
	copy(msg[4:11], me[:])
	// Parity is the remainder of the 88 message bits augmented with 24
	// zeros; filling it back makes the whole-frame syndrome zero.
	aug := append(append([]byte(nil), msg[:11]...), 0, 0, 0)
	parity := modesSyndrome(aug, 112)
	msg[11] = byte(parity >> 16)
	msg[12] = byte(parity >> 8)
	msg[13] = byte(parity)
	return strings.ToUpper(hex.EncodeToString(msg))
}

// setMEBits writes n bits of v starting at ME bit offset start.
func setMEBits(me *[7]byte, start, n int, v uint64) {
	for i := 0; i < n; i++ {
		if v>>(uint(n-1-i))&1 == 1 {
			p := start + i
			me[p/8] |= 1 << uint(7-p%8)
		}
	}
}

// TestModeSSurfacePositionOffsets: surface frames (TC 5-8) carry movement
// at ME bits 5..11, the ground-track status at bit 12 and the track at
// bits 13..19. The airborne-position offsets used before read altitude
// bits instead.
func TestModeSSurfacePositionOffsets(t *testing.T) {
	var me [7]byte
	setMEBits(&me, 0, 5, 6)    // TC 6: surface position
	setMEBits(&me, 5, 7, 20)   // movement code 20 -> 2 + 0.5*(20-13) = 5.5 kt
	setMEBits(&me, 12, 1, 1)   // ground track status: valid
	setMEBits(&me, 13, 7, 64)  // track 64 -> 180 degrees
	setMEBits(&me, 20, 1, 0)   // time flag
	setMEBits(&me, 21, 1, 1)   // CPR odd
	setMEBits(&me, 22, 17, 10) // arbitrary CPR lat
	setMEBits(&me, 39, 17, 20) // arbitrary CPR lon
	m, err := DecodeModeS(buildReviewDF17(0xABCDEF, me))
	if err != nil {
		t.Fatal(err)
	}
	if m.TC != 6 {
		t.Fatalf("tc = %d, want 6", m.TC)
	}
	if !m.HasSurface {
		t.Fatal("surface flag not set")
	}
	if m.GroundSpeedKt != 5.5 {
		t.Errorf("ground speed = %.3f kt, want 5.5 (movement code 20)", m.GroundSpeedKt)
	}
	if m.SurfaceTrack != 180 {
		t.Errorf("surface track = %.2f, want 180", m.SurfaceTrack)
	}
	if !m.CPROdd {
		t.Error("CPR parity lost: frame is odd")
	}

	// With the track-status bit clear the track bits mean nothing.
	var meNoTrack [7]byte
	setMEBits(&meNoTrack, 0, 5, 6)
	setMEBits(&meNoTrack, 5, 7, 20)
	setMEBits(&meNoTrack, 12, 1, 0)
	setMEBits(&meNoTrack, 13, 7, 64)
	m, err = DecodeModeS(buildReviewDF17(0xABCDEF, meNoTrack))
	if err != nil {
		t.Fatal(err)
	}
	if m.SurfaceTrack != 0 {
		t.Errorf("track = %.2f with status clear, want 0", m.SurfaceTrack)
	}
	if m.GroundSpeedKt != 5.5 {
		t.Errorf("ground speed = %.3f, want 5.5 still", m.GroundSpeedKt)
	}
}

// TestModeSVelocityUnavailable: in TC19 a velocity component field of 0
// means "no information", not "-1 kt".
func TestModeSVelocityUnavailable(t *testing.T) {
	build := func(sub int, ewSign, ew, nsSign, ns uint64) string {
		var me [7]byte
		setMEBits(&me, 0, 5, 19)          // TC 19
		setMEBits(&me, 5, 3, uint64(sub)) // subtype
		setMEBits(&me, 13, 1, ewSign)
		setMEBits(&me, 14, 10, ew)
		setMEBits(&me, 24, 1, nsSign)
		setMEBits(&me, 25, 10, ns)
		return buildReviewDF17(0x485020, me)
	}

	// Subtype 1 with the east-west field zero: velocity unavailable.
	m, err := DecodeModeS(build(1, 0, 0, 0, 161))
	if err != nil {
		t.Fatal(err)
	}
	if m.HasVel {
		t.Errorf("HasVel with EW field 0: speed=%.1f track=%.1f", m.SpeedKt, m.TrackDeg)
	}
	// Same with the north-south field zero.
	m, err = DecodeModeS(build(1, 0, 9, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if m.HasVel {
		t.Errorf("HasVel with NS field 0: speed=%.1f track=%.1f", m.SpeedKt, m.TrackDeg)
	}
	// Both fields present: EW = 9-1 = 8 kt, NS = 161-1 = 160 kt, both
	// positive directions -> speed sqrt(8^2+160^2) ~ 160.2.
	m, err = DecodeModeS(build(1, 0, 9, 0, 161))
	if err != nil {
		t.Fatal(err)
	}
	if !m.HasVel {
		t.Fatal("valid velocity not decoded")
	}
	if m.SpeedKt < 159.7 || m.SpeedKt > 160.7 {
		t.Errorf("speed = %.2f kt, want ~160.2", m.SpeedKt)
	}
	// Subtype 3 (airspeed) with the airspeed field zero: unavailable.
	m, err = DecodeModeS(build(3, 1, 694, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if m.HasVel {
		t.Errorf("HasVel with airspeed field 0: speed=%.1f", m.SpeedKt)
	}
	// Subtype 3 with airspeed 376: heading 694*360/1024 ~ 243.98, speed
	// 376-1 = 375 kt (cross-checked against mode-s.org "The 1090MHz
	// Riddle" subtype 3/4 worked example).
	m, err = DecodeModeS(build(3, 1, 694, 0, 376))
	if err != nil {
		t.Fatal(err)
	}
	if !m.HasVel {
		t.Fatal("valid airspeed not decoded")
	}
	if m.SpeedKt != 375 {
		t.Errorf("airspeed = %.1f kt, want 375", m.SpeedKt)
	}
	if m.TrackDeg < 243.9 || m.TrackDeg > 244.1 {
		t.Errorf("heading = %.2f, want ~243.98", m.TrackDeg)
	}
}
