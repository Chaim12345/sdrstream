package decode

import (
	"os"
	"strings"
	"testing"
)

// TestPOCSAGRealRecording runs on a real 1200 bps POCSAG recording when
// SDRSTREAM_POCSAG_WAV points at one (Wikimedia Commons "POCSAG sample -
// 1200 bps", converted to WAV). Only BCH-clean pages may be returned.
func TestPOCSAGRealRecording(t *testing.T) {
	p := os.Getenv("SDRSTREAM_POCSAG_WAV")
	if p == "" {
		t.Skip("SDRSTREAM_POCSAG_WAV not set")
	}
	s, rate, err := ReadWAV(p)
	if err != nil {
		t.Fatal(err)
	}
	msgs := POCSAGDecode(s, rate, 0)
	if len(msgs) != 1 || !strings.Contains(msgs[0].String(), "TIME=0008300324") {
		t.Fatalf("unexpected pages: %v", msgs)
	}
}
