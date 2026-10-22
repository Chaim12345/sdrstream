package decode

import (
	"encoding/hex"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

// Payloads independently recovered by ft8_lib's C decoder from the official
// WSJT 181201_180245 recording. Its type-3 text unpacker returns ERROR_TYPE.
func TestRTTYRealPayloads(t *testing.T) {
	for payload, want := range map[string]string{
		"4ceaf08f3cada187d358": "KV7J WD9HSY 529 IL",
		"738cb1fa92313dc7d498": "WB3LGC AA5AU R 529 LA",
		"4b262d84b678ae5fd318": "KC8YDS KF6CRW R 559 ID",
		"053b3d1f56ff8f47da98": "N9LF WV4P R 529 TN",
		"4ce53cdd7400e68fd8d8": "KV4ZY NX8G 539 OH",
		"773108185267979fd198": "XE1MEX N0UX 559 CO",
	} {
		b, _ := hex.DecodeString(payload)
		var p [10]byte
		copy(p[:], b)
		text, typ := unpackFTX(p, &callHashes{})
		if typ != "rtty" || text != want {
			t.Fatalf("%s: %q %s", payload, text, typ)
		}
	}
}

func TestWeakSignalNoFalsePositives(t *testing.T) {
	for _, seed := range []int64{1, 2, 3, 7} {
		r := rand.New(rand.NewSource(seed))
		s := make([]float64, 180000)
		for i := range s {
			s[i] = r.NormFloat64() * 0.01
		}
		if got := FTXDecode(s, 12000, "FT8"); len(got) != 0 {
			t.Fatalf("noise seed %d: %+v", seed, got)
		}
	}
	if got := FTXDecode(make([]float64, 180000), 12000, "FT8"); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestSuppressionRetainsFirstPass(t *testing.T) {
	s, r, e := ReadWAV("testdata/ft8/test_05.wav")
	if e != nil {
		t.Fatal(e)
	}
	w := buildWaterfall(resampleTo12k(s, r), protoFT8)
	first := map[string]bool{}
	for _, c := range w.findCandidates() {
		p, _, ok := w.decodeCandidate(c)
		if ok {
			text, _ := unpackFTX(p, &callHashes{})
			first[text] = true
		}
	}
	for _, m := range FTXDecode(s, r, "FT8") {
		delete(first, m.Text)
	}
	if len(first) > 0 {
		t.Fatalf("lost original decodes: %v", first)
	}
}

func TestFlexReassembleBoundaries(t *testing.T) {
	first := FlexMessage{Capcode: 1400141, Frame: 12, Fragment: 'F', fragmentID: 3, Text: "Rotterdam Rij"}
	tail := FlexMessage{Capcode: 1400141, Frame: 13, Fragment: 'C', fragmentID: 0, Text: "nmond."}
	got := FlexReassemble([]FlexMessage{first, tail})
	if len(got) != 1 || got[0].Text != "Rotterdam Rijnmond." || got[0].Fragment != 'K' {
		t.Fatal(got)
	}
	for _, in := range [][]FlexMessage{{first}, {tail}, {tail, first}} {
		if got := FlexReassemble(in); len(got) != 0 {
			t.Fatal(got)
		}
	}
	for _, bad := range []FlexMessage{
		{Capcode: 1400285, Frame: 13, Fragment: 'C', fragmentID: 0},
		{Capcode: 1400141, Frame: 30, Fragment: 'C', fragmentID: 0},
		{Capcode: 1400141, Frame: 13, Fragment: 'C', fragmentID: 1},
	} {
		if got := FlexReassemble([]FlexMessage{first, bad}); len(got) != 0 {
			t.Fatal(got)
		}
	}
}

// Optional real fixture, obtained by decoderegress -fetch. No network in tests.
func TestFlexP2000Reassembly(t *testing.T) {
	p := filepath.Join(os.TempDir(), "sdrstream-regress", "FLEX_1600_2fsk_P2000_proef_alarm.wav")
	if _, e := os.Stat(p); e != nil {
		t.Skip("run decoderegress -fetch to cache the real fixture")
	}
	s, r, e := ReadWAV(p)
	if e != nil {
		t.Fatal(e)
	}
	raw := FlexDecode(s, r)
	if len(raw) != 40 {
		t.Fatalf("raw pages %d, want 40", len(raw))
	}
	found := map[int64]bool{}
	for _, m := range FlexReassemble(raw) {
		if m.Capcode == 1400141 || m.Capcode == 1400285 {
			if m.Text != "Test: Proefalarm Ochtend Brandweer Veiligheidsregio Rotterdam Rijnmond." {
				t.Fatal(m)
			}
			found[m.Capcode] = true
		}
	}
	if len(found) != 2 {
		t.Fatal(found)
	}
}
