package decode

import (
	"context"
	"strings"
	"testing"
)

// The auto stream wrappers must label every decode with the engine that
// produced it. With no reference binaries resolvable, that is the Go
// decoder; the label must be explicit ("go"), never empty.
func TestStreamAutoLabelsGoFallback(t *testing.T) {
	t.Setenv("SDRSTREAM_REFDEC_DIR", t.TempDir())
	t.Setenv("PATH", t.TempDir())

	// 31 s of silence at 12 kHz starting on an FT8 slot boundary: two full
	// slots, no messages, but the engine path is exercised end to end.
	silence := make([]float64, 12000*31)
	msgs, engine, err := FTXDecodeStreamAuto(context.Background(), silence, 12000, 1792646925.0, "FT8")
	if err != nil {
		t.Fatal(err)
	}
	if engine != "go" {
		t.Fatalf("FTXDecodeStreamAuto engine=%q, want go", engine)
	}
	for _, m := range msgs {
		if m.Engine != "go" {
			t.Fatalf("message engine=%q, want go", m.Engine)
		}
		if m.SlotUnix == 0 {
			t.Fatalf("SlotUnix not set: %+v", m)
		}
	}

	windows, wengine, err := WSPRWindowsAuto(context.Background(), make([]float64, 12000*130), 12000, 1792646880.0)
	if err != nil {
		t.Fatal(err)
	}
	if wengine != "go" {
		t.Fatalf("WSPRWindowsAuto engine=%q, want go", wengine)
	}
	for _, w := range windows {
		for _, m := range w.Messages {
			if m.Engine != "go" {
				t.Fatalf("message engine=%q, want go", m.Engine)
			}
		}
	}
}

// With jt9 installed the live FT8 capture decodes through the reference
// engine and every message is labelled "jt9".
func TestStreamAutoPrefersJT9(t *testing.T) {
	d := JT9("")
	if d.Binary() == "" {
		t.Skip("jt9 not installed")
	}
	samples, rate, err := ReadWAV("testdata/ft8/live_websdr_utwente_7074kHz_20261022T052841Z.wav")
	if err != nil {
		t.Skip("live capture fixture absent")
	}
	msgs, engine, err := FTXDecodeStreamAuto(context.Background(), samples, rate, 1792646921.15, "FT8")
	if err != nil {
		t.Fatal(err)
	}
	if engine != "jt9" {
		t.Fatalf("engine=%q, want jt9", engine)
	}
	if len(msgs) == 0 {
		t.Fatal("jt9 decoded nothing from a busy-band capture")
	}
	for _, m := range msgs {
		if m.Engine != "jt9" {
			t.Fatalf("message engine=%q, want jt9", m.Engine)
		}
	}
}

// With wsprd installed the live WSPR capture decodes through the reference
// engine and every spot is labelled "wsprd".
func TestWindowsAutoPrefersWSPRD(t *testing.T) {
	d := WSPRD("")
	if d.Binary() == "" {
		t.Skip("wsprd not installed")
	}
	samples, rate, err := ReadWAV("testdata/wspr/live_websdr_utwente_7038p6kHz_20261022T0532Z.wav")
	if err != nil {
		t.Skip("live capture fixture absent")
	}
	windows, engine, err := WSPRWindowsAuto(context.Background(), samples, rate, 1792647120.0)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	for _, w := range windows {
		for _, m := range w.Messages {
			n++
			if m.Engine != "wsprd" {
				t.Fatalf("message engine=%q, want wsprd", m.Engine)
			}
			if m.WindowT == 0 {
				t.Fatalf("WindowT not set: %+v", m)
			}
			if !strings.Contains(m.Text, " ") && m.Grid == "" {
				t.Fatalf("spot without grid: %+v", m)
			}
		}
	}
	if engine != "wsprd" {
		t.Fatalf("engine=%q, want wsprd", engine)
	}
	if n == 0 {
		t.Fatal("wsprd decoded nothing from a live WSPR capture")
	}
}
