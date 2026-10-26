package decode

// ax100.go: GOMspace AX100 "RS mode" deframer (used by GOMX-1/3/4, AAUSAT,
// and others). Frame layout, from the format documented by gr-satellites
// (GPL-3.0) and re-implemented here: after the G3RUH descrambler, the
// 32-bit sync word 0x930B51DE, then one length byte L, then L-1 bytes that
// form a shortened RS(255,223) codeword (conventional basis, CCSDS
// generator): L-33 payload bytes + 32 parity.

// AX100Sync is the AX100 sync word.
const AX100Sync uint32 = 0x930B51DE

// AX100Frame is one RS-verified AX100 frame.
type AX100Frame struct {
	Offset    int
	Corrected int
	Payload   []byte
}

// AX100RSDeframe descrambles (G3RUH), searches for the sync word allowing
// syncErr bit errors, and returns frames whose RS check passes.
func AX100RSDeframe(bits []bool, syncErr int) []AX100Frame {
	d := G3RUHDescramble(bits)
	var out []AX100Frame
	for off := 0; off+40 <= len(d); off++ {
		var w uint32
		for i := 0; i < 32; i++ {
			w <<= 1
			if d[off+i] {
				w |= 1
			}
		}
		if popcount32(w^AX100Sync) > syncErr {
			continue
		}
		p := off + 32
		var L int
		for i := 0; i < 8; i++ {
			L <<= 1
			if d[p+i] {
				L |= 1
			}
		}
		if L < rsNroots+2 || L > 255 || p+8+(L-1)*8 > len(d) {
			continue
		}
		cw := make([]byte, L-1)
		for i := range cw {
			for b := 0; b < 8; b++ {
				cw[i] <<= 1
				if d[p+8+i*8+b] {
					cw[i] |= 1
				}
			}
		}
		n, err := RSDecode(cw)
		if err != nil {
			continue
		}
		out = append(out, AX100Frame{Offset: off, Corrected: n, Payload: cw[:len(cw)-rsNroots]})
	}
	return out
}
