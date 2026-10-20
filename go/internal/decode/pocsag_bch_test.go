package decode

import "testing"

func pocsagEncode(data uint32) uint32 {
	d := data & 0x1FFFFF
	var parity uint32
	for i := 0; i < 21; i++ {
		if d&(1<<uint(i)) != 0 {
			parity ^= pocsagParityTbl[i]
		}
	}
	cw := (d << 11) | (parity << 1)
	if parity32(cw) != 0 {
		cw |= 1
	}
	return cw
}

func TestPocsagBCHVectors(t *testing.T) {
	// Ground truth from multimon-ng's bch_pocsag_encode (mm_bch.c).
	cases := []struct{ in, want uint32 }{
		{0x0000000, 0x00000000},
		{0x0000001, 0x00000ED3},
		{0x0034567, 0x1A2B3E0C},
		{0x01FFFFF, 0xFFFFFFFF},
		{0x00ABCDE, 0x55E6F6DA},
		{1000000 << 3, 0xD090037F},
		{(1000000 << 3) | 3, 0xD0901EDA},
		{(1 << 20) | 0xABCDE, 0xD5E6F1B3},
	}
	for _, c := range cases {
		if got := pocsagEncode(c.in); got != c.want {
			t.Errorf("encode(%07X) = %08X, want %08X", c.in, got, c.want)
		}
	}
}

func TestPocsagBCHCorrects(t *testing.T) {
	// Corrupt two bits of a valid codeword and confirm correction restores it.
	cw := pocsagEncode((1000000 << 3) | 3)
	for _, err := range []uint32{1 << 5, 1 << 17, (1 << 3) | (1 << 25)} {
		if got := pocsagCorrect(cw ^ err); got != cw {
			t.Errorf("correct(%08X ^ %08X) = %08X, want %08X", cw, err, got, cw)
		}
	}
}
