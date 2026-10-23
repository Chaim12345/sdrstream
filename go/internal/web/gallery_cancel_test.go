package web

import (
	"bytes"
	"encoding/binary"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/gallery"
)

// TestServerShutdownCancelsInFlightGalleryDecode proves the server's
// lifecycle context reaches an in-flight gallery decode: cancel arrives
// 5 ms into a decode that takes far longer, and the artefact is left
// with a cancel reason, no frame, and no partial PNG on disk. The audio
// is loud noise, not a real signal: the decode stages (envelope, sync
// search) still run the cancellable loops.
func TestServerShutdownCancelsInFlightGalleryDecode(t *testing.T) {
	dir := t.TempDir()
	s := New("", dir)
	// 450 s of noise at 11025 Hz: inside all gallery input caps.
	rate := 11025
	n := 450 * rate
	rng := rand.New(rand.NewSource(3))
	var buf bytes.Buffer
	buf.WriteString("RIFF")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(36+n*2))
	buf.WriteString("WAVEfmt ")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(16))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(1))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(1))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(rate))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(rate*2))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(2))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(16))
	buf.WriteString("data")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(n*2))
	for i := 0; i < n; i++ {
		v := int16(3000 * math.Sin(2*math.Pi*2400*float64(i)/float64(rate)) * float64(rng.Intn(2000)) / 1000)
		_ = binary.Write(&buf, binary.LittleEndian, v)
	}
	name := "shutdown-cancel.wav"
	if err := os.WriteFile(filepath.Join(dir, name), buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	items := []gallery.Item{{Kind: "audio", Mode: "APT", Name: name, Path: filepath.Join(dir, name), Source: "satnogs"}}
	time.AfterFunc(5*time.Millisecond, s.cancel)
	frames := s.decodeGallery(items)
	if len(frames) != 0 {
		t.Fatalf("cancelled server decode produced %d frame(s)", len(frames))
	}
	if !strings.Contains(items[0].DecodeStatus, "cancelled") {
		t.Fatalf("cancel reason missing on artefact: %q", items[0].DecodeStatus)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "*.png"))
	if len(matches) != 0 {
		t.Fatalf("partial frame left on disk: %v", matches)
	}
}
