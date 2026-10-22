package hfdl

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// TestRealSamples decodes public HFDL audio recordings (8 kHz mono WAV from
// signals.taunus.de) when they are present in $HFDL_SAMPLES. Each must yield
// a frame whose FCS verifies.
func TestRealSamples(t *testing.T) {
	dir := os.Getenv("HFDL_SAMPLES")
	if dir == "" {
		t.Skip("HFDL_SAMPLES not set")
	}
	for _, f := range []string{"300", "600", "1200"} {
		b, err := os.ReadFile(filepath.Join(dir, "HFDL-"+f+"bps.WAV"))
		if err != nil {
			t.Skip(err)
		}
		var x []float64
		for i := 44; i+1 < len(b); i += 2 {
			x = append(x, float64(int16(binary.LittleEndian.Uint16(b[i:])))/32768)
		}
		if got := DecodeAudio(x, 8000, SSBCarrierOffsetHz, 0); len(got) == 0 {
			t.Errorf("%s bps: no frame with valid FCS", f)
		}
	}
}
