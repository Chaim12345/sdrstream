package decode

// Reference-decoder tests are env-gated: they run only when the tools are
// installed (SDRSTREAM_REFDEC_DIR or PATH) and use committed recordings, so
// they stay network-free.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func refTestCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	return ctx
}

func TestRefDecodersResolutionIsStable(t *testing.T) {
	names := map[string]bool{}
	for _, d := range RefDecoders() {
		if d.Name() == "" || len(d.Modes()) == 0 {
			t.Errorf("decoder missing name or modes: %+v", d)
		}
		if names[d.Name()] {
			t.Errorf("duplicate decoder name %q", d.Name())
		}
		names[d.Name()] = true
	}
	for _, want := range []string{"jt9", "wsprd", "ft8lib", "multimon-ng", "acarsdec"} {
		if !names[want] {
			t.Errorf("registry missing %s", want)
		}
	}
}

func TestWSPRDDecodeLiveCapture(t *testing.T) {
	d := WSPRD("")
	if d.Binary() == "" {
		t.Skip("wsprd not installed")
	}
	msgs, err := d.DecodeWAV(refTestCtx(t), "testdata/wspr/live_websdr_utwente_7038p6kHz_20261022T0532Z.wav", "")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"F4BAD JN25 20": true, "E79Q JN83 23": true, "F4WCW JN05 30": true}
	for _, m := range msgs {
		delete(want, m.Text)
	}
	for missing := range want {
		t.Errorf("wsprd missed %q (got %d messages)", missing, len(msgs))
	}
}

// TestJT9DecodeFT8Corpus runs the WSJT-X jt9 reference decoder over the
// pinned FT8 corpus (manifest ft8 entries with a reference decode list) and
// requires jt9 to reproduce every reference message not listed in the
// fixture's ref_known_misses. The lists are WSJT-X GUI decodes of these
// recordings; the pinned allowlist lines were not reproduced by the
// installable CLI jt9 builds tried (2.2.2, 2.5.4, 2.7.0, all at
// jt9 -8 -d 3, 2026-10-24 - see manifest.json jt9_leg); the cause is
// unconfirmed (GUI a-priori / KVASD-class decodes is a hypothesis, not
// proven), and one line IS reproduced by 2.7.0 - the allowlist entries
// carry the per-version truth. The gate is calibrated and valid for CLI
// jt9 2.2.2-2.5.4 behaviour: jt9 2.7.0 misses four further
// non-allowlisted lines, so this test fails under 2.7.0 by design until
// the allowlist is re-validated for a newer jt9. Every other miss means the reference tool
// or the corpus changed - per project rule that is REPORTED, never tuned
// away. A pinned known miss must still exist in the reference list, so
// list drift cannot hide behind the exemption. A reference line may carry a
// trailing annotation ("CQ TA6CQ KN70      AS Turkey"); jt9 never prints
// annotations, so a message matches when it equals the reference text or
// extends it after a space. Skips when jt9 is not installed.
func TestJT9DecodeFT8Corpus(t *testing.T) {
	d := JT9("")
	if d.Binary() == "" {
		t.Skip("jt9 not installed")
	}
	m := loadDecoderManifest(t)
	ran := 0
	for _, f := range m.Fixtures {
		if f.Kind != "ft8" || f.RefFile == "" {
			continue
		}
		t.Run(f.Name, func(t *testing.T) {
			ref := wsjtMessages(t, filepath.Join("testdata", f.RefFile))
			msgs, err := d.DecodeWAV(refTestCtx(t), filepath.Join("testdata", f.Name+".wav"), "ft8")
			if err != nil {
				t.Fatal(err)
			}
			got := make([]string, 0, len(msgs))
			for _, msg := range msgs {
				got = append(got, msg.Text)
			}
			known := map[string]bool{}
			for _, k := range f.RefKnownMisses {
				if !ref[k.Message] {
					t.Errorf("known miss %q is not in the reference list - stale exemption", k.Message)
				}
				known[k.Message] = true
			}
			for want := range ref {
				ok := false
				for _, g := range got {
					if g == want || strings.HasPrefix(g, want+" ") {
						ok = true
						break
					}
				}
				switch {
				case !ok && !known[want]:
					t.Errorf("jt9 missed reference decode %q (got %d messages)", want, len(got))
				case !ok && known[want]:
					t.Logf("known miss as documented (not reproduced by CLI jt9; cause unconfirmed): %q", want)
				case ok && known[want]:
					t.Logf("note: allowlisted line %q now reproduces - jt9 behaviour changed, review the exemption", want)
				}
			}
			ran++
		})
	}
	if ran == 0 {
		t.Fatal("manifest pins no ft8 corpus entries with reference lists")
	}
}

func TestEngineFallsBackToGo(t *testing.T) {
	t.Setenv("SDRSTREAM_DECODE_ENGINE", "ref")
	// Point the resolver at an empty dir: every ref tool is unavailable, so
	// the auto path must fall back to the Go decoder instead of failing.
	t.Setenv("SDRSTREAM_REFDEC_DIR", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	samples := make([]float64, 12000*15)
	if _, engine, err := FTXDecodeAuto(context.Background(), samples, 12000, "FT8"); err != nil || engine != "go" {
		t.Errorf("FTXDecodeAuto: engine=%q err=%v, want go", engine, err)
	}
	if _, engine, err := WSPRDecodeAuto(context.Background(), make([]float64, 12000*120), 12000); err != nil || engine != "go" {
		t.Errorf("WSPRDecodeAuto: engine=%q err=%v, want go", engine, err)
	}
}
