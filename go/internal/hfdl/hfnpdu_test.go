package hfdl

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestACARSCRCandParity(t *testing.T) {
	// Build a block: mode 2, reg, NAK, label H1, block id '3', STX text ETX.
	body := []byte("2.N12345")
	body = append(body, 0x15, 'H', '1', 'A', 0x02, 'H', 'I', 0x03)
	for i := range body {
		p := byte(0)
		for v := body[i]; v != 0; v &= v - 1 {
			p ^= 1
		}
		if p == 0 {
			body[i] |= 0x80
		}
	}
	c := crc16CCITT(body, 0)
	blk := append([]byte{1}, body...)
	blk = append(blk, byte(c), byte(c>>8), 0x7f)
	a := ParseACARS(blk, false)
	if a == nil || !a.CRCOK || a.Reg != ".N12345" || a.Label != "H1" || a.Text != "HI" || !a.Final {
		t.Fatalf("bad parse: %+v", a)
	}
	blk[5] ^= 0x01
	if a := ParseACARS(blk, false); a == nil || a.CRCOK {
		t.Fatal("corrupted block passed CRC")
	}
}

func TestCoord(t *testing.T) {
	if got := coord(0x7ffff); got < 179.99 || got > 180.01 {
		t.Fatal(got)
	}
	if got := coord(0x80000); got > -179.9 {
		t.Fatal(got)
	}
}

func TestRealPerfData(t *testing.T) {
	dir := os.Getenv("HFDL_SAMPLES")
	if dir == "" {
		t.Skip("HFDL_SAMPLES not set")
	}
	b, err := os.ReadFile(filepath.Join(dir, "HFDL-300bps.WAV"))
	if err != nil {
		t.Skip(err)
	}
	var x []float64
	for i := 44; i+1 < len(b); i += 2 {
		x = append(x, float64(int16(binary.LittleEndian.Uint16(b[i:])))/32768)
	}
	for _, d := range DecodeAudio(x, 8000, SSBCarrierOffsetHz, 0) {
		for _, l := range d.PDU.LPDUs {
			if h := ParseHFNPDU(l.Payload, true); h != nil && h.Type == HFNPerformanceData && !h.Err {
				t.Logf("perf data: flight %q lat %.2f lon %.2f utc %s gs %d", h.FlightID, h.Lat, h.Lon, h.UTC, h.GSID)
				return
			}
		}
	}
	t.Error("no performance data HFNPDU decoded from the 300 bps sample")
}
