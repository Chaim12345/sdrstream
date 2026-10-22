package decode

// Reference-decoder tests are env-gated: they run only when the tools are
// installed (SDRSTREAM_REFDEC_DIR or PATH) and use committed recordings, so
// they stay network-free.

import (
	"context"
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

func TestJT9DecodeFT8Corpus(t *testing.T) {
	d := JT9("")
	if d.Binary() == "" {
		t.Skip("jt9 not installed")
	}
	wav := "../../../tests/data/real_aprs_wa8lmf_30s.wav" // smoke: runs, may decode nothing
	if _, err := d.DecodeWAV(refTestCtx(t), wav, "ft8"); err != nil {
		t.Fatal(err)
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
