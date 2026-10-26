package iqsrc

import (
	"encoding/binary"
	"testing"
)

// buildIQFrame synthesizes a wire frame exactly per rx_sound.h snd_pkt_iq_t.
func buildIQFrame(flags uint8, seq uint32, smeter uint16, payload []byte) []byte {
	f := make([]byte, KiwiSNDHeaderLen, KiwiSNDHeaderLen+len(payload))
	copy(f[:3], "SND")
	f[3] = flags
	binary.LittleEndian.PutUint32(f[4:8], seq)
	binary.BigEndian.PutUint16(f[8:10], smeter)
	f[10] = 7
	binary.LittleEndian.PutUint32(f[12:16], 1234567)
	binary.LittleEndian.PutUint32(f[16:20], 890)
	return append(f, payload...)
}

func beIQ(iq [][2]int16) []byte {
	b := make([]byte, 0, len(iq)*4)
	for _, p := range iq {
		var t [4]byte
		binary.BigEndian.PutUint16(t[0:2], uint16(p[0]))
		binary.BigEndian.PutUint16(t[2:4], uint16(p[1]))
		b = append(b, t[:]...)
	}
	return b
}

func TestParseKiwiIQFrameBigEndian(t *testing.T) {
	payload := beIQ([][2]int16{{16384, -16384}, {-1, 1}})
	frame := buildIQFrame(0x08, 42, 1200, payload) // smeter 1200 -> -7.0 dBm
	dst := make([]complex64, 8)
	h, n, err := ParseKiwiIQFrame(frame, dst)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("samples=%d want 2", n)
	}
	if h.Seq != 42 || h.GPSSec != 1234567 || h.GPSNSec != 890 || h.LastGPSSolution != 7 {
		t.Fatalf("header: %+v", h)
	}
	if d := h.SmeterDBm(); d < -7.01 || d > -6.99 {
		t.Fatalf("smeter %v want -7.0", d)
	}
	if real(dst[0]) != 0.5 || imag(dst[0]) != -0.5 {
		t.Fatalf("sample0 %v", dst[0])
	}
	if h.LittleEndian {
		t.Fatal("BE frame marked LE")
	}
}

func TestParseKiwiIQFrameLittleEndianFlag(t *testing.T) {
	var p [4]byte
	binary.LittleEndian.PutUint16(p[0:2], 0x4000) // I = 16384
	binary.LittleEndian.PutUint16(p[2:4], 0xC000) // Q = -16384
	frame := buildIQFrame(0x08|0x80, 1, 1270, p[:])
	dst := make([]complex64, 1)
	h, n, err := ParseKiwiIQFrame(frame, dst)
	if err != nil || n != 1 {
		t.Fatalf("err=%v n=%d", err, n)
	}
	if !h.LittleEndian || real(dst[0]) != 0.5 || imag(dst[0]) != -0.5 {
		t.Fatalf("LE decode: %+v %v", h, dst[0])
	}
}

func TestParseKiwiIQFrameRejectsNonIQ(t *testing.T) {
	// Audio frame: STEREO clear (typical audio flags 0x00/0x01 etc).
	audio := buildIQFrame(0x00, 1, 1270, make([]byte, 64))
	if _, _, err := ParseKiwiIQFrame(audio, nil); err == nil {
		t.Fatal("audio frame accepted as IQ")
	}
	// Truncated header (10-byte AUDIO header length).
	if _, _, err := ParseKiwiIQFrame(audio[:10], nil); err == nil {
		t.Fatal("truncated header accepted")
	}
	// Not SND.
	bad := buildIQFrame(0x08, 1, 1270, make([]byte, 8))
	copy(bad[:3], "W/F")
	if _, _, err := ParseKiwiIQFrame(bad, nil); err == nil {
		t.Fatal("non-SND accepted")
	}
	// Odd payload.
	if _, _, err := ParseKiwiIQFrame(buildIQFrame(0x08, 1, 1270, make([]byte, 6)), nil); err == nil {
		t.Fatal("non-multiple-of-4 payload accepted")
	}
	// Compressed flag on claimed IQ.
	if _, _, err := ParseKiwiIQFrame(buildIQFrame(0x18, 1, 1270, make([]byte, 8)), nil); err == nil {
		t.Fatal("compressed flag accepted on IQ")
	}
	// Empty payload.
	if _, _, err := ParseKiwiIQFrame(buildIQFrame(0x08, 1, 1270, nil), nil); err == nil {
		t.Fatal("empty payload accepted")
	}
}

func TestParseKiwiIQFrameEndiannessDiscriminates(t *testing.T) {
	// Same 4 payload bytes must decode differently under the two flags;
	// this is the property the len-10 byte counter could never check.
	var p [4]byte
	binary.BigEndian.PutUint16(p[0:2], 0x00FF) // BE: I=255; LE: I=-256
	binary.BigEndian.PutUint16(p[2:4], 0x0100) // BE: Q=256; LE: Q=1
	be, _, errB := ParseKiwiIQFrame(buildIQFrame(0x08, 1, 1270, p[:]), make([]complex64, 1))
	le, _, errL := ParseKiwiIQFrame(buildIQFrame(0x88, 1, 1270, p[:]), make([]complex64, 1))
	if errB != nil || errL != nil {
		t.Fatal(errB, errL)
	}
	_ = be
	_ = le
	dstB := make([]complex64, 1)
	dstL := make([]complex64, 1)
	ParseKiwiIQFrame(buildIQFrame(0x08, 1, 1270, p[:]), dstB)
	ParseKiwiIQFrame(buildIQFrame(0x88, 1, 1270, p[:]), dstL)
	if dstB[0] == dstL[0] {
		t.Fatal("endianness flag made no difference")
	}
}
