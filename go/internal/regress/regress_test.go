package regress

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const moduleRoot = "../.."

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"  CQ  DX \tK1ABC  FN42 ":  "CQ DX K1ABC FN42",
		"4552|3|  hello<EOT><EOT>": "4552|3|hello",
		"<...> KO85HX 23":          "<...> KO85HX 23",
		"a\x00b\x01c":              "a b c",
		"1|3| x | y":               "1|3|x|y",
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCompareStatuses(t *testing.T) {
	ref := map[string]bool{"a": true, "b": true, "c": true}
	ok := map[string]bool{"x": true}
	run := func(got []string, floor, maxExtra int) Result {
		g := map[string]bool{}
		for _, s := range got {
			g[s] = true
		}
		r := Result{Case: Case{Floor: floor, MaxExtra: maxExtra}}
		compare(&r, g, ref, ok)
		return r
	}
	if r := run([]string{"a", "b", "c"}, -1, 0); r.Status != Pass {
		t.Errorf("full match = %s", r.Status)
	}
	if r := run([]string{"a", "b"}, 2, 0); r.Status != Gap || len(r.Missed) != 1 {
		t.Errorf("above floor = %s %v", r.Status, r.Missed)
	}
	if r := run([]string{"a"}, 2, 0); r.Status != Fail {
		t.Errorf("below floor = %s", r.Status)
	}
	if r := run([]string{"a", "b", "c", "x"}, -1, 0); r.Status != Pass || len(r.Bonus) != 1 {
		t.Errorf("second-decoder bonus = %s bonus=%v", r.Status, r.Bonus)
	}
	if r := run([]string{"a", "b", "c", "zz"}, -1, 0); r.Status != Fail {
		t.Errorf("unconfirmed extra beyond baseline = %s", r.Status)
	}
	if r := run([]string{"a", "b", "c", "zz"}, -1, 1); r.Status != Gap {
		t.Errorf("tolerated extra = %s", r.Status)
	}
}

func TestManifestConsistent(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range Manifest() {
		if seen[c.Name] {
			t.Errorf("duplicate case %s", c.Name)
		}
		seen[c.Name] = true
		if c.Local == "" && (c.URL == "" || c.File == "" || c.SHA256 == "") {
			t.Errorf("%s: remote case needs URL, File and pinned SHA256", c.Name)
		}
		if c.Golden != "" {
			if _, err := loadGolden(c.Golden); err != nil {
				t.Errorf("%s: golden %s: %v", c.Name, c.Golden, err)
			}
		} else if c.Negative {
			if c.MinFrames != 0 {
				t.Errorf("%s: negative case with MinFrames", c.Name)
			}
		} else if c.MinFrames == 0 && c.Decoder != "wspr" {
			t.Errorf("%s: no golden and no MinFrames", c.Name)
		}
	}
	for name := range baseline() {
		if !seen[name] {
			t.Errorf("baseline names unknown case %s", name)
		}
	}
}

// The committed recordings run offline in every CI pass.
func TestLocalCases(t *testing.T) {
	n := 0
	for _, c := range Manifest() {
		if c.Local == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(moduleRoot, c.Local)); err != nil {
			t.Logf("%s: fixture absent, skipping", c.Name)
			continue
		}
		n++
		r := Run(c, "", moduleRoot)
		if r.Status == Fail || r.Status == Skip {
			t.Errorf("%s: %s %s missed=%v extra=%v", c.Name, r.Status, r.Note, r.Missed, r.Extra)
		}
	}
	if n == 0 {
		t.Skip("no committed recordings present")
	}
}

// With a populated cache (decoderegress -fetch) the whole manifest runs.
func TestFullManifest(t *testing.T) {
	dir := os.Getenv("SDRSTREAM_REGRESS_CACHE")
	if dir == "" {
		t.Skip("SDRSTREAM_REGRESS_CACHE not set")
	}
	var rs []Result
	for _, c := range Manifest() {
		if c.Local == "" {
			if _, err := os.Stat(filepath.Join(dir, c.File)); err != nil {
				t.Errorf("%s: %s missing from cache", c.Name, c.File)
				continue
			}
			if err := verify(c, filepath.Join(dir, c.File)); err != nil && !strings.HasSuffix(c.URL, ".flac") {
				t.Error(err)
			}
		}
		r := Run(c, dir, moduleRoot)
		rs = append(rs, r)
		if r.Status == Fail {
			t.Errorf("%s: FAIL matched %d/%d (floor %d) extra %d (max %d) %s", c.Name, r.Matched, r.Expected, c.Floor, len(r.Extra), c.MaxExtra, r.Note)
		}
	}
	var b bytes.Buffer
	WriteTable(&b, rs)
	t.Log("\n" + b.String())
}

func TestMissingRecordingFails(t *testing.T) {
	r := Run(Case{Name: "x", Decoder: "pocsag", File: "nope.wav", Golden: "pocsag_sample_512.txt"}, t.TempDir(), moduleRoot)
	if r.Status != Fail {
		t.Fatalf("missing file status = %s", r.Status)
	}
}

func TestTableAndBaselineOutput(t *testing.T) {
	rs := []Result{{Case: Case{Name: "c1", Decoder: "ft8", Golden: "g", Ref: "WSJT-X"}, Expected: 3, Got: 2, Matched: 2, Missed: []string{"m"}, Status: Gap}}
	var b bytes.Buffer
	WriteTable(&b, rs)
	WriteDetails(&b, rs)
	for _, want := range []string{"STATUS", "c1", "GAP", "missed: m", "0 PASS, 1 GAP"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("table lacks %q:\n%s", want, b.String())
		}
	}
	b.Reset()
	WriteBaseline(&b, rs)
	if !strings.Contains(b.String(), "c1 2 0") {
		t.Errorf("baseline = %q", b.String())
	}
}
