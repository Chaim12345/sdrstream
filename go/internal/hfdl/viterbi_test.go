package hfdl

import "testing"

func TestViterbiRoundTrip(t *testing.T) {
	data := []byte{0x12, 0x34, 0xAB, 0xFF, 0x00, 0x5A, 0xC3, 0x00}
	syms := viterbiEncode(data)
	got := viterbiDecode(syms, len(data)*8)
	for i := range data {
		if reverseByte(got[i]) != data[i] {
			t.Fatalf("byte %d: got %02x want %02x (raw %x)", i, reverseByte(got[i]), data[i], got)
		}
	}
}
