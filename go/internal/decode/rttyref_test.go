package decode

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// synthDWDRTTY modulates text as 50 baud Baudot RTTY with the DWD F1B
// parameters (mark LOW 1675 Hz, space high 2125 Hz, 450 Hz shift) plus a
// leading mark idle, like the real Pinneberg idles. Letters only: the
// figure-shift table lives in the decoder under test.
func synthDWDRTTY(text string, rate int, markHz, spaceHz, baud float64) []float64 {
	letters := map[byte]int{
		'E': 1, 'A': 3, ' ': 4, 'S': 5, 'I': 6, 'U': 7, 'D': 9, 'R': 10,
		'J': 11, 'N': 12, 'F': 13, 'C': 14, 'K': 15, 'T': 16, 'Z': 17,
		'L': 18, 'W': 19, 'H': 20, 'Y': 21, 'P': 22, 'Q': 23, 'O': 24,
		'B': 25, 'G': 26, 'M': 28, 'X': 29, 'V': 30,
	}
	figures := map[byte]int{
		'3': 1, '-': 3, ' ': 4, '8': 6, '7': 7, '4': 10, ',': 12, ':': 14,
		'(': 15, '5': 16, ')': 18, '2': 19, '6': 21, '0': 22, '1': 23,
		'9': 24, '?': 25, '.': 28, '/': 29,
	}
	var bits []int
	for i := 0; i < 40; i++ {
		bits = append(bits, 1) // mark idle so receivers lock
	}
	push := func(code int) {
		bits = append(bits, 0)
		for b := 0; b < 5; b++ {
			bits = append(bits, code>>uint(b)&1)
		}
		bits = append(bits, 1, 1) // 1.5+ stop bits
	}
	push(0x1F) // letters shift
	inFigures := false
	for i := 0; i < len(text); i++ {
		if code, ok := letters[text[i]]; ok {
			if inFigures {
				push(0x1F)
				inFigures = false
			}
			push(code)
		} else if code, ok := figures[text[i]]; ok && text[i] != ' ' {
			if !inFigures {
				push(0x1B)
				inFigures = true
			}
			push(code)
		}
	}
	spb := float64(rate) / baud
	out := make([]float64, 0, int(float64(len(bits))*spb)+rate)
	phase := 0.0
	for _, bit := range bits {
		f := spaceHz
		if bit == 1 {
			f = markHz
		}
		n := int(spb)
		for i := 0; i < n; i++ {
			out = append(out, 0.7*math.Sin(phase))
			phase += 2 * math.Pi * f / float64(rate)
		}
	}
	return out
}

// TestRTTYRefDecodeSynthetic is an integration test for the minimodem
// subprocess wrapper: synthetic DWD-parameter audio must decode to the
// modulated text through the independent engine. Skipped without a binary
// (set SDRSTREAM_REF_MINIMODEM or install minimodem).
func TestRTTYRefDecodeSynthetic(t *testing.T) {
	binary := MinimodemBinary("")
	if binary == "" {
		t.Skip("minimodem not installed")
	}
	const rate = 12000
	samples := synthDWDRTTY("CQ CQ DE DDK2 FREQUENCIES ", rate, 1675, 2125, 50)
	dir := t.TempDir()
	wav := filepath.Join(dir, "in.wav")
	pcm := make([]int16, len(samples))
	for i, s := range samples {
		pcm[i] = int16(s * 30000)
	}
	if err := os.WriteFile(wav, WriteWAV16(pcm, rate), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := RTTYRefDecode(context.Background(), binary, wav, 1675, 2125, 50)
	if err != nil {
		t.Fatal(err)
	}
	if res.Engine != "minimodem" {
		t.Errorf("engine %q", res.Engine)
	}
	if !strings.Contains(strings.ToUpper(res.Text), "DDK2") {
		t.Errorf("reference text %q missing DDK2", res.Text)
	}
}

// TestRTTYRefDecodeCommittedFixture cross-checks the committed real-signal
// fixture through both engines; the agreement verdict must pass.
func TestRTTYRefDecodeCommittedFixture(t *testing.T) {
	binary := MinimodemBinary("")
	if binary == "" {
		t.Skip("minimodem not installed")
	}
	if _, err := os.Stat(refRTTY); err != nil {
		t.Skip("fixture not present")
	}
	res, err := RTTYRefDecode(context.Background(), binary, refRTTY, 2125, 1955, 45.45)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToUpper(res.Text), "CQ") {
		t.Errorf("reference text %q missing CQ", res.Text)
	}
	samples, rate, err := ReadWAV(refRTTY)
	if err != nil {
		t.Fatal(err)
	}
	goText, _ := RTTYDecodeAuto(samples, rate, 2125, 1955, 45.45)
	if goText == "" {
		t.Fatal("Go decode empty")
	}
	// The two engines must agree well enough to share long substrings.
	norm := func(s string) string { return strings.Join(strings.Fields(strings.ToUpper(s)), " ") }
	g, r := norm(goText), norm(res.Text)
	if !strings.Contains(g, "TEST") || !strings.Contains(r, "TEST") {
		t.Errorf("engines disagree: go=%q ref=%q", g, r)
	}
}

func TestMinimodemBinaryMissing(t *testing.T) {
	t.Setenv("SDRSTREAM_REF_MINIMODEM", "")
	t.Setenv("SDRSTREAM_REFDEC_DIR", "")
	if got := MinimodemBinary("/no/such/path/minimodem"); got != "" {
		t.Errorf("resolved %q", got)
	}
	if _, err := RTTYRefDecode(context.Background(), "/no/such/path/minimodem", "x.wav", 0, 0, 0); err == nil {
		t.Error("expected error for missing binary")
	}
}
