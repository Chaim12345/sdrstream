package gallery

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// seedAudio copies the committed off-air APRS fixture into dir under name
// and returns the item path. The fixture is fast to attempt under every
// decoder branch, which keeps the mode matrix decisive and cheap.
func seedAudio(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(realAPRSFixture)
	if err != nil {
		t.Skip("off-air APRS fixture not present:", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// Every mode string must route to a real decoder branch or be left
// completely untouched - never coerced into a speculative decode. The
// SatNOGS mode vocabulary is grounded in the live SatNOGS DB transmitter
// list (AFSK, APT, BPSK, CW, FM, FMN, FSK, GMSK, HRPT, LRPT, LSB, PSK,
// USB, ...). Routing is proven by the branch-specific outcome: an AFSK
// branch decodes packets from packet audio, a CW branch reports "no
// plausible Morse" for 0.8 ms packet elements, an untouched mode keeps
// an empty status and produces nothing.
func TestDecodeFramesModeMatrix(t *testing.T) {
	cases := []struct {
		mode       string
		wantBranch string // "afsk", "cw", or "untouched"
	}{
		{"AFSK", "afsk"}, {"APRS", "afsk"}, {"AX25", "afsk"}, {"AFSK1200", "afsk"},
		{"afsk", "afsk"},            // mode matching is case-insensitive
		{"CW", "cw"}, {"MCW", "cw"}, // MCW reaches the Morse branch, which has the two-tone MCW fallback
		{"FM", "untouched"}, {"FMN", "untouched"}, {"USB", "untouched"}, {"LSB", "untouched"},
		{"BPSK", "untouched"}, {"FSK", "untouched"}, {"GMSK", "untouched"},
		{"HRPT", "untouched"}, {"LRPT", "untouched"}, // no HRPT/LRPT decoder exists; neither matches the APT substring gate
		{"speech", "untouched"}, {"voice", "untouched"}, {"", "untouched"},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			dir := t.TempDir()
			name := "matrix-" + strings.ToLower(strings.ReplaceAll(tc.mode, " ", "_")) + ".wav"
			items := []Item{{Kind: "audio", Mode: tc.mode, Name: name, Path: seedAudio(t, dir, name), Source: "satnogs"}}
			frames := DecodeFrames(items, dir)
			switch tc.wantBranch {
			case "afsk":
				if len(frames) != 1 || frames[0].Kind != "text" {
					t.Fatalf("mode %q: got %d artefacts, want 1 decoded text artefact (status %q)", tc.mode, len(frames), items[0].DecodeStatus)
				}
			case "cw":
				if len(frames) != 0 {
					t.Fatalf("mode %q: got %d artefacts from packet audio, want 0", tc.mode, len(frames))
				}
				if !strings.Contains(items[0].DecodeStatus, "no plausible Morse") {
					t.Fatalf("mode %q: status %q, want the CW branch's no-plausible-Morse note", tc.mode, items[0].DecodeStatus)
				}
			case "untouched":
				if len(frames) != 0 {
					t.Fatalf("mode %q: produced %d artefacts, want none", tc.mode, len(frames))
				}
				if items[0].DecodeStatus != "" {
					t.Fatalf("mode %q: unsupported mode must not be attempted, got status %q", tc.mode, items[0].DecodeStatus)
				}
			}
		})
	}
}

// Concurrent decode runs must not share mutable state: two galleries
// decoding the same fixture into separate directories both succeed.
func TestDecodeFramesConcurrent(t *testing.T) {
	var wg sync.WaitGroup
	results := make([][]Item, 2)
	statuses := make([]string, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			dir := t.TempDir()
			name := "concurrent.wav"
			items := []Item{{Kind: "audio", Mode: "AFSK", Name: name, Path: seedAudio(t, dir, name), Source: "satnogs"}}
			results[n] = DecodeFrames(items, dir)
			statuses[n] = items[0].DecodeStatus
		}(i)
	}
	wg.Wait()
	for n := range results {
		if len(results[n]) != 1 || statuses[n] != "" {
			t.Fatalf("run %d: got %d artefacts, status %q; want 1 clean decode", n, len(results[n]), statuses[n])
		}
	}
}
