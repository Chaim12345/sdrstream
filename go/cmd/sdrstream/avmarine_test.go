package main

import (
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/decode"
)

// Published even/odd airborne CPR pair for ICAO 40621D (mode-s.org "The
// 1090 Megahertz Riddle" worked example).
const (
	cprEven40621D = "8D40621D58C382D690C8AC2863A7"
	cprOdd40621D  = "8D40621D58C386435CC412692AD6"
)

// buildOtherICAORetimed re-addresses a published CPR frame to a different
// ICAO24 and fixes the parity, for cross-aircraft pairing probes.
func readdressFrame(t *testing.T, frame string, icao uint32) string {
	t.Helper()
	msg, err := hex.DecodeString(frame)
	if err != nil {
		t.Fatal(err)
	}
	msg[1] = byte(icao >> 16)
	msg[2] = byte(icao >> 8)
	msg[3] = byte(icao)
	msg[11], msg[12], msg[13] = 0, 0, 0
	// Mode S parity: remainder of the 88 message bits with 24
	// augmentation zeros.
	var rem uint32
	bits := append(append([]byte(nil), msg[:11]...), 0, 0, 0)
	for i := 0; i < 112; i++ {
		bit := uint32(bits[i/8]>>(7-uint(i%8))) & 1
		top := rem >> 23
		rem = ((rem << 1) | bit) & 0xFFFFFF
		if top == 1 {
			rem ^= 0xFFF409
		}
	}
	msg[11] = byte(rem >> 16)
	msg[12] = byte(rem >> 8)
	msg[13] = byte(rem)
	return strings.ToUpper(hex.EncodeToString(msg))
}

func mustModeS(t *testing.T, frame string) *decode.ModeSMessage {
	t.Helper()
	m, err := decode.DecodeModeS(frame)
	if err != nil {
		t.Fatalf("%s: %v", frame, err)
	}
	if !m.HasCPR {
		t.Fatalf("%s: no CPR", frame)
	}
	return m
}

// TestCPRPairKeyedByICAO: interleaved frames from two aircraft must not be
// paired with each other; each aircraft pairs only its own even/odd.
func TestCPRPairKeyedByICAO(t *testing.T) {
	trk := newCPRPairTracker()
	now := time.Now()
	even := mustModeS(t, cprEven40621D)
	odd := mustModeS(t, cprOdd40621D)
	other := mustModeS(t, readdressFrame(t, cprOdd40621D, 0xABC123))

	if pair, _ := trk.add(even, now); pair != nil {
		t.Fatal("paired after a single frame")
	}
	if pair, _ := trk.add(other, now); pair != nil {
		t.Fatal("paired two different aircraft")
	}
	pair, fresh := trk.add(odd, now)
	if pair == nil {
		t.Fatal("same-aircraft even/odd did not pair")
	}
	if !fresh {
		t.Fatal("simultaneous pair reported stale")
	}
	lat, lon, err := decode.CPRPosition(pair.even, pair.odd, pair.evenWhen, pair.oddWhen)
	if err != nil {
		t.Fatal(err)
	}
	if lat < 52.2 || lat > 52.3 || lon < 3.8 || lon > 4.0 {
		t.Errorf("position %.5f,%.5f: want the published 52.257,3.919", lat, lon)
	}
	if pair.even.ICAO24 != "40621D" {
		t.Errorf("paired icao = %s, want 40621D", pair.even.ICAO24)
	}
}

// TestCPRPairWindow: even/odd frames more than 10 s apart must not decode
// to a position; the aircraft may have crossed a latitude zone.
func TestCPRPairWindow(t *testing.T) {
	trk := newCPRPairTracker()
	t0 := time.Unix(1_700_000_000, 0)
	even := mustModeS(t, cprEven40621D)
	odd := mustModeS(t, cprOdd40621D)

	trk.add(even, t0)
	pair, fresh := trk.add(odd, t0.Add(15*time.Second))
	if pair == nil {
		t.Fatal("both parities on hand, want the pair back")
	}
	if fresh {
		t.Error("15 s separation reported fresh, want stale (window is 10 s)")
	}
	// A newer odd frame within the window refreshes the pair.
	pair, fresh = trk.add(odd, t0.Add(9*time.Second))
	if pair == nil || !fresh {
		t.Error("9 s separation should pair fresh")
	}
}
