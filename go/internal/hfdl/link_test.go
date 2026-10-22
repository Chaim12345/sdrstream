package hfdl

import "testing"

func addFCS(b []byte) []byte {
	c := crc16CCITT(b, 0xFFFF) ^ 0xFFFF
	return append(append([]byte{}, b...), byte(c), byte(c>>8))
}

func TestParseDownlinkLogon(t *testing.T) {
	lp := addFCS([]byte{LPDULogonRequestNormal, 0x4B, 0xAA, 0x01})
	hdr := []byte{0x02 | 1<<2, 0x05, 0x21, 0, 0, 0, byte(len(lp) - 1)}
	frame := append(addFCS(hdr), lp...)
	p := ParsePDU(frame, 8927000)
	if !p.CRCOK || p.Direction != Downlink || p.SrcID != 0x21 || p.DstID != 5 || len(p.LPDUs) != 1 {
		t.Fatalf("bad parse: %v", p)
	}
	if l := p.LPDUs[0]; !l.CRCOK || l.ICAO != 0x4BAA01 {
		t.Fatalf("bad lpdu: %+v", l)
	}
	frame[3] ^= 0xFF
	if q := ParsePDU(frame, 0); q.CRCOK || len(q.LPDUs) != 0 {
		t.Fatalf("corrupted frame accepted: %v", q)
	}
}
