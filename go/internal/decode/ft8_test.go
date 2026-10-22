package decode

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Real over-the-air FT8 recordings (20 m, 2019-11-11) from the ft8_lib test
// suite (MIT). The .txt next to each WAV is the WSJT-X decode list. Every
// message that WSJT-X produced and that the Go decoder reports must match
// exactly, and the decoder must recover most of them.
var wsjtLine = regexp.MustCompile(`~\s+(.*?)(\s{2,}.*)?$`)

func wsjtMessages(t *testing.T, path string) map[string]bool {
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, l := range strings.Split(string(b), "\n") {
		if m := wsjtLine.FindStringSubmatch(l); m != nil {
			out[strings.TrimSpace(m[1])] = true
		}
	}
	return out
}

func TestFT8RealRecordings(t *testing.T) {
	files, _ := filepath.Glob("testdata/ft8/*.wav")
	if len(files) == 0 {
		t.Skip("no recordings")
	}
	totalRef, totalHit, totalExtra := 0, 0, 0
	for _, f := range files {
		samples, rate, err := ReadWAV(f)
		if err != nil {
			t.Fatal(err)
		}
		ref := wsjtMessages(t, strings.TrimSuffix(f, ".wav")+".txt")
		got := FTXDecode(samples, rate, "FT8")
		gotSet := map[string]bool{}
		for _, m := range got {
			gotSet[m.Text] = true
		}
		for r := range ref {
			totalRef++
			if gotSet[r] {
				totalHit++
			} else {
				t.Logf("%s: missed %q", filepath.Base(f), r)
			}
		}
		for g := range gotSet {
			if !ref[g] {
				totalExtra++
				t.Logf("%s: not in WSJT-X list: %q", filepath.Base(f), g)
			}
		}
	}
	t.Logf("matched %d of %d WSJT-X decodes, %d extra", totalHit, totalRef, totalExtra)
	if totalHit*10 < totalRef*7 {
		t.Fatalf("recovered only %d of %d reference decodes", totalHit, totalRef)
	}
}

func TestFTXCRCSelfCheck(t *testing.T) {
	// ft8_lib/WSJT-X: all-zero payload has CRC 0
	if c := ftxCRC(make([]byte, 12), 82); c != 0 {
		t.Fatalf("crc %d", c)
	}
}

func TestFFTMatchesDFT(t *testing.T) {
	n := 60
	x := make([]complex128, n)
	for i := range x {
		x[i] = complex(float64(i%7)-3, float64(i%5)-2)
	}
	y := append([]complex128(nil), x...)
	newFFTPlan(n).transform(y)
	for k := 0; k < n; k++ {
		var s complex128
		for j := 0; j < n; j++ {
			s += x[j] * newFFTPlan(n).twiddle[(j*k)%n]
		}
		d := s - y[k]
		if real(d)*real(d)+imag(d)*imag(d) > 1e-12 {
			t.Fatalf("bin %d: %v vs %v", k, s, y[k])
		}
	}
}

// Synthetic FT4 fixture generated with ft8_lib gen_ft8 (not over the air).
// The FT4 decoder has no real-audio verification yet; this only proves the
// protocol constants round-trip against an independent encoder.
func TestFT4SyntheticFixture(t *testing.T) {
	samples, rate, err := ReadWAV("testdata/ft8/synthetic_ft4_K1ABC_W9XYZ_-11.wav.fixture")
	if err != nil {
		t.Fatal(err)
	}
	got := FTXDecode(samples, rate, "FT4")
	if len(got) != 1 || got[0].Text != "K1ABC W9XYZ -11" || got[0].Protocol != "FT4" {
		t.Fatalf("got %+v", got)
	}
	if got[0].FreqHz < 990 || got[0].FreqHz > 1010 {
		t.Fatalf("freq %v", got[0].FreqHz)
	}
}
