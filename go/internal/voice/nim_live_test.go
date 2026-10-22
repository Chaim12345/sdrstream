package voice

import (
	"context"
	"os"
	"testing"

	"github.com/chaim12345/sdrstream/go/internal/decode"
)

// TestNIMLive is the live check against the hosted NVIDIA endpoint. It is
// skipped unless SDRSTREAM_NIM_LIVE=1 and needs the real API key in
// SDRSTREAM_NIM_API_KEY and a speech WAV in SDRSTREAM_NIM_LIVE_WAV. It
// exists so a multilingual clip can be verified against the actual
// service; the mock tests above pin everything else.
func TestNIMLive(t *testing.T) {
	if os.Getenv("SDRSTREAM_NIM_LIVE") != "1" {
		t.Skip("set SDRSTREAM_NIM_LIVE=1 with SDRSTREAM_NIM_API_KEY and SDRSTREAM_NIM_LIVE_WAV to run the live check")
	}
	options := DefaultNIMOptions()
	if options.APIKey == "" {
		t.Skip("SDRSTREAM_NIM_API_KEY not set")
	}
	path := os.Getenv("SDRSTREAM_NIM_LIVE_WAV")
	samples, rate, err := decode.ReadWAV(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	transcript, err := TranscribeNIM(context.Background(), samples, rate, options)
	if err != nil {
		t.Fatalf("live NIM call failed: %v", err)
	}
	t.Logf("backend=%s seconds=%.1f transcript=%q", transcript.Backend, transcript.Seconds, transcript.Text)
	if transcript.Text == "" {
		t.Fatal("live endpoint returned an empty transcript for real speech")
	}
}
