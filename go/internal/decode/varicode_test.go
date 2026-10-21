package decode

import (
	"strings"
	"testing"
)

// idleBits builds the PSK31 idle pattern of n bits (0x55 repeated).
func idleBits(n int) string {
	var sb strings.Builder
	for i := 0; i < n; i++ {
		if i%2 == 0 {
			sb.WriteByte('0')
		} else {
			sb.WriteByte('1')
		}
	}
	return sb.String()
}

// TestVaricodeIdleLengths checks that the idle between characters may be
// any multiple of the 16-bit pattern. A transmitter that waits after the
// last character sends a longer idle, and the decoder used to reject the
// whole message because of it.
func TestVaricodeIdleLengths(t *testing.T) {
	for _, idleLength := range []int{16, 32, 64} {
		bits := ""
		for _, ch := range "CQ DE RS44" {
			bits += idleBits(idleLength)
			bits += varicode[ch]
		}
		bits += idleBits(idleLength)
		text, invalid, idles := decodeVaricodeStrict(bits)
		if invalid != 0 {
			t.Errorf("idle %d bits: %d invalid segments", idleLength, invalid)
		}
		if idles == 0 {
			t.Errorf("idle %d bits: no idle found", idleLength)
		}
		if !strings.Contains(text, "RS44") {
			t.Errorf("idle %d bits: text = %q, want it to contain RS44", idleLength, text)
		}
	}
}

// TestVaricodeRejectsGarbage checks that a random bit stream is not
// mistaken for a varicode message.
func TestVaricodeRejectsGarbage(t *testing.T) {
	bits := ""
	seed := uint32(99)
	for i := 0; i < 400; i++ {
		seed = seed*1664525 + 1013904223
		if (seed>>16)&1 == 1 {
			bits += "1"
		} else {
			bits += "0"
		}
	}
	_, invalid, _ := decodeVaricodeStrict(bits)
	if invalid == 0 {
		t.Error("random bits decoded as a clean varicode stream")
	}
}
