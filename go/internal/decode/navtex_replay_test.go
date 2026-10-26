package decode

import (
	"encoding/binary"
	"os"
	"strings"
	"testing"
)

// wavPCM16 mono reader for replay fixtures (RIFF/WAVE, PCM16, mono).
func readReplayWAV(t *testing.T, path string) ([]float64, int) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("replay fixture not present: %v", err)
	}
	if len(b) < 44 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		t.Fatal("not RIFF/WAVE")
	}
	rate := int(binary.LittleEndian.Uint32(b[24:28]))
	channels := int(binary.LittleEndian.Uint16(b[22:24]))
	bits := int(binary.LittleEndian.Uint16(b[34:36]))
	if channels != 1 || bits != 16 {
		t.Fatalf("want mono PCM16, got ch=%d bits=%d", channels, bits)
	}
	var data []byte
	for off := 12; off+8 <= len(b); {
		size := int(binary.LittleEndian.Uint32(b[off+4 : off+8]))
		body := off + 8
		if string(b[off:off+4]) == "data" {
			data = b[body : body+size]
			break
		}
		off = body + size + (size & 1)
	}
	if data == nil {
		t.Fatal("no data chunk")
	}
	out := make([]float64, len(data)/2)
	for i := range out {
		out[i] = float64(int16(binary.LittleEndian.Uint16(data[2*i:]))) / 32768
	}
	return out, rate
}

// Offline replay of a REAL recorded NAVTEX capture (Oostende 490 kHz,
// 2026-10-26 08:10 UTC, the user's own receiver - provenance in
// testdata/navtex_oostende_490.provenance.md). Asserts the decoder
// reproduces the message text recorded at capture time. This is real-signal
// evidence for the decoder stage; it says nothing about live receiver
// reachability.
func TestNAVTEXReplayRecordedCapture(t *testing.T) {
	path := os.Getenv("NAVTEX_REPLAY_WAV")
	if path == "" {
		path = "testdata/navtex_oostende_490.wav"
	}
	samples, rate := readReplayWAV(t, path)
	msgs := NAVTEXDecode(samples, rate, 1700) // dial 488.3 kHz USB, mark 1785 / space 1615
	if len(msgs) == 0 {
		t.Fatal("no NAVTEX messages decoded from the recorded capture")
	}
	var all strings.Builder
	for _, m := range msgs {
		all.WriteString(m.Text)
		all.WriteByte('\n')
	}
	// Text is the body between the ZCZC header and NNNN, so frame markers
	// are not part of it; assert recorded body content instead.
	for _, want := range []string{"OOSTENDERADIO", "THAMES"} {
		if !strings.Contains(all.String(), want) {
			t.Fatalf("replay decode missing %q; got %d messages", want, len(msgs))
		}
	}
	t.Logf("replayed %.0fs @%dHz -> %d messages", float64(len(samples))/float64(rate), rate, len(msgs))
}
