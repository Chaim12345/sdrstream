package decode

import (
	"math"
	"math/big"
	"math/rand"
	"testing"
	"time"
)

// R5: audio-level WSPR SNR fixtures, same pattern as the FT8 ladder: a
// fully synthetic WSPR signal generated in-test (callsign/grid/power
// packing -> convolutional K=32 rate-1/2 encode -> bit-reversal
// interleave -> 4-FSK with the sync vector), mixed with calibrated AWGN
// (2500 Hz reference bandwidth, same derivation as ft8_snr_test.go),
// through the production WSPRDecode. Labelled synthetic AWGN: no fading
// or drift, an upper bound on real performance.
//
// Reference for the threshold: WSJT-X Version 2.7 User Guide - WSPR
// "can be decoded at signal-to-noise ratios as low as -31 dB in a 2500
// Hz bandwidth" (wsjt.sourceforge.io/wsjtx-doc/wsjtx-main-2.7.0.html).
// That figure describes WSJT-X's own decoder on real signals; the
// comparison is indicative, not exact.

// wsprPackCall inverts wsprUnpackCall for a standard call with its
// single digit in the third position (e.g. "K1JT" padded " K1JT ").
func wsprPackCall(t *testing.T, padded string) int32 {
	t.Helper()
	if len(padded) != 6 {
		t.Fatalf("need 6-char padded call, got %q", padded)
	}
	idx := func(c byte) int {
		for i := 0; i < len(wsprChars); i++ {
			if wsprChars[i] == c {
				return i
			}
		}
		return -1
	}
	c0, c1, c2 := idx(padded[0]), idx(padded[1]), idx(padded[2])
	c3 := idx(padded[3]) - 10
	c4 := idx(padded[4]) - 10
	c5 := idx(padded[5]) - 10
	if c0 < 0 || c1 < 0 || c1 > 35 || c2 < 0 || c2 > 9 || c3 < 0 || c4 < 0 || c5 < 0 {
		t.Fatalf("call %q has characters outside the WSPR alphabet positions", padded)
	}
	n := (((((int32(c0)*36)+int32(c1))*10+int32(c2))*27+int32(c3))*27+int32(c4))*27 + int32(c5)
	call, ok := wsprUnpackCall(n)
	if !ok || call != padded[:5] && call != string([]byte{padded[1], padded[2], padded[3], padded[4]}) {
		// unpack trims spaces; compare against the trimmed form
		trimmed := padded
		for len(trimmed) > 0 && trimmed[0] == ' ' {
			trimmed = trimmed[1:]
		}
		for len(trimmed) > 0 && trimmed[len(trimmed)-1] == ' ' {
			trimmed = trimmed[:len(trimmed)-1]
		}
		if !ok || call != trimmed {
			t.Fatalf("pack round trip: %q -> %d -> %q (ok=%v)", padded, n, call, ok)
		}
	}
	return n
}

// wsprGridBits brute-forces the grid integer that unpacks to grid.
func wsprGridBits(t *testing.T, grid string) int32 {
	t.Helper()
	for n := int32(0); n < 32400; n++ {
		if g, ok := wsprUnpackGrid(n << 7); ok && g == grid {
			return n
		}
	}
	t.Fatalf("no grid integer unpacks to %q", grid)
	return 0
}

// wsprEncodeMessage packs call/grid/dBm into the 81-bit message and runs
// the K=32 rate-1/2 convolutional code (wsprPoly1/2) MSB-first,
// interleaves by the inverse of wsprDeinterleave, and returns 162
// channel symbols (tone = 2*data + sync).
func wsprEncodeMessage(t *testing.T, call, grid string, dbm int) [162]uint8 {
	t.Helper()
	if dbm%10 != 0 && dbm%10 != 3 && dbm%10 != 7 {
		t.Fatalf("type-1 power must end in 0, 3 or 7: %d", dbm)
	}
	n1 := wsprPackCall(t, call)
	n2 := wsprGridBits(t, grid)<<7 | int32(dbm+64)
	// 50 bits: n1(28) || n2(22), then 31 zero tail bits.
	msg := new(big.Int).Lsh(big.NewInt(int64(n1)), 22)
	msg.Or(msg, big.NewInt(int64(n2)))
	var bits [81]uint8
	for i := 0; i < 50; i++ {
		bits[i] = uint8(msg.Bit(49 - i))
	}
	// Convolutional encode: 32-bit shift register, one output pair per
	// input bit.
	var enc uint32
	var coded [162]uint8
	for i := 0; i < 81; i++ {
		enc = (enc << 1) | uint32(bits[i])
		v := wsprEncode(enc)
		coded[2*i] = uint8(v >> 1)
		coded[2*i+1] = uint8(v & 1)
	}
	// Interleave: deinterleave maps tmp[p] = sym[rev8(i)] for kept j, so
	// the forward direction writes data[p] into position j.
	var inter [162]uint8
	p := 0
	for i := 0; p < 162; i++ {
		j := int(reverse8test(uint8(i)))
		if j < 162 {
			inter[j] = coded[p]
			p++
		}
	}
	// Round-trip check against the production deinterleave.
	round := inter
	wsprDeinterleave(&round)
	for i := 0; i < 162; i++ {
		if round[i] != coded[i] {
			t.Fatalf("interleave is not the inverse of wsprDeinterleave at %d", i)
		}
	}
	var sym [162]uint8
	for i := 0; i < 162; i++ {
		sym[i] = inter[i]*2 + uint8(wsprSync[i])
	}
	return sym
}

func reverse8test(b uint8) uint8 {
	// Local copy of bits.Reverse8 to keep the intent explicit.
	b = (b&0x55)<<1 | (b&0xAA)>>1
	b = (b&0x33)<<2 | (b&0xCC)>>2
	return b<<4 | b>>4
}

// wsprModulate renders the symbols as continuous-phase 4-FSK at 12 kHz:
// 8192 samples per 1.4648 baud symbol, spacing 12000/8192 Hz.
func wsprModulate(sym [162]uint8, f0 float64) []float64 {
	const rate, spb = 12000, 8192
	df := float64(rate) / spb
	out := make([]float64, 162*spb)
	phase := 0.0
	for i, s := range sym {
		freq := f0 + float64(s)*df
		for k := 0; k < spb; k++ {
			out[i*spb+k] = math.Cos(phase)
			phase += 2 * math.Pi * freq / rate
		}
	}
	return out
}

func TestWSPRSyntheticSNR(t *testing.T) {
	sym := wsprEncodeMessage(t, " K1JT ", "FN42", 10)
	clean := wsprModulate(sym, 1500)
	// One second of quiet before the burst, matching the nominal start
	// one second into the even-minute window.
	pad := make([]float64, 12000)
	full := append(pad, clean...)
	start := time.Now()
	got := WSPRDecode(full, 12000)
	elapsed := time.Since(start)
	found := false
	for _, m := range got {
		if m.Call == "K1JT" && m.Grid == "FN42" && m.DBm == 10 {
			found = true
		}
	}
	t.Logf("noiseless decode: %v (%d messages, %s)", found, len(got), elapsed.Round(time.Millisecond))
	if !found {
		t.Fatalf("noiseless synthetic decode failed: %+v", got)
	}
	// Measured AWGN behavior (2026-10-23, this test's generator, 3 seeds
	// per point): -28 dB 3/3 decode, -29 dB 3/3, -30 dB 1/3, -31 dB 0/3.
	// Practical threshold is about -29/-30 dB; the WSJT-X 2.7 guide's
	// reference is -31 dB (2500 Hz) for WSJT-X's own decoder on real
	// signals, so this decoder sits within about 1-2 dB of it on a
	// friendly synthetic channel - indicative, not exact.
	decodeAt := func(snr float64, seed int64) bool {
		rng := rand.New(rand.NewSource(seed))
		for _, m := range WSPRDecode(ft8MixNoise(full, snr, rng), 12000) {
			if m.Call == "K1JT" && m.Grid == "FN42" {
				return true
			}
		}
		return false
	}
	for _, snr := range []float64{-28, -29, -30, -31} {
		hits := 0
		for seed := int64(1); seed <= 3; seed++ {
			if decodeAt(snr, seed) {
				hits++
			}
		}
		t.Logf("boundary SNR %+5.1f dB: %d/3 seeds decode", snr, hits)
	}
	for seed := int64(1); seed <= 3; seed++ {
		if !decodeAt(-24, seed) {
			t.Errorf("seed %d: -24 dB decode failed; decoder regressed well inside its measured band", seed)
		}
		if decodeAt(-36, seed) {
			t.Errorf("seed %d: -36 dB decode succeeded; deep-noise hallucination guard broken", seed)
		}
	}
}
