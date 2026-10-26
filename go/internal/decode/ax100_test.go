package decode

import (
	"bytes"
	"encoding/hex"
	"math/rand"
	"os"
	"strings"
	"testing"
)

// Provenance: RS codewords come from libfec encode_rs_8 (independent,
// offline); the AX100 framing around them (sync, length byte, scrambler)
// is assembled by this test from the documented layout, so the frame
// level is SYNTHETIC, not an over-the-air capture.

func TestRSShortenedLibfec(t *testing.T) {
	raw, _ := os.ReadFile("testdata/rs_shortened_libfec_vectors.txt")
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 3 {
		t.Fatal("vectors")
	}
	for _, ln := range lines {
		cw, _ := hex.DecodeString(strings.Fields(ln)[1])
		if !bytes.Equal(RSEncode(cw[:len(cw)-32]), cw) {
			t.Fatalf("%s encode mismatch", ln[:8])
		}
		bad := append([]byte{}, cw...)
		bad[0] ^= 0xFF
		bad[len(bad)-1] ^= 0x11
		if n, err := RSDecode(bad); err != nil || n != 2 || !bytes.Equal(bad, cw) {
			t.Fatalf("%s decode n=%d err=%v", ln[:8], n, err)
		}
	}
}

func TestAX100RSDeframe(t *testing.T) {
	raw, _ := os.ReadFile("testdata/rs_shortened_libfec_vectors.txt")
	cw, _ := hex.DecodeString(strings.Fields(strings.Split(string(raw), "\n")[0])[1])
	payload := cw[:len(cw)-32]
	L := byte(len(cw) + 1)
	frame := append([]byte{0x93, 0x0B, 0x51, 0xDE, L}, cw...)
	r := rand.New(rand.NewSource(9))
	var pre []bool
	for i := 0; i < 53; i++ {
		pre = append(pre, r.Intn(2) == 1)
	}
	plain := append(pre, bitsOf(frame)...)
	plain = append(plain, bitsOf([]byte{0, 0, 0, 0})...)
	tx := G3RUHScramble(plain)
	// Corrupt 4 payload bytes' worth of bits (each flip also disturbs
	// two later descrambled bits: stay well under 16 byte errors).
	for _, p := range []int{53 + 60, 53 + 100, 53 + 140} {
		tx[p] = !tx[p]
	}
	got := AX100RSDeframe(tx, 2)
	if len(got) != 1 || !bytes.Equal(got[0].Payload, payload) || got[0].Corrected == 0 {
		t.Fatalf("frames=%d", len(got))
	}
	noise := make([]bool, 4000)
	for i := range noise {
		noise[i] = r.Intn(2) == 1
	}
	if len(AX100RSDeframe(noise, 2)) != 0 {
		t.Fatal("noise decoded")
	}
}
