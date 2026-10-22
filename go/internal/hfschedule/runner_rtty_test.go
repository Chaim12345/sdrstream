package hfschedule

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/decode"
)

// fakeWAV is one second of non-silent PCM audio (sine), matching the 1s
// Listen the tests configure.
func fakeWAV() []byte {
	pcm := make([]int16, 8000)
	for i := range pcm {
		pcm[i] = int16(12000 * math.Sin(2*math.Pi*440*float64(i)/8000))
	}
	return decode.WriteWAV16(pcm, 8000)
}

// TestRunOnceRTTYCrossCheckVerified: a recorded RTTY capture whose Go decode
// and reference decode agree is marked verified, with the reference text
// saved beside the Go text and the ledger carrying the verified fields.
func TestRunOnceRTTYCrossCheckVerified(t *testing.T) {
	dir := t.TempDir()
	target := decode.HFTarget{Name: "DWD 1 Pinneberg RTTY", Kind: "rtty", CarrierKHz: 4583, DialKHz: 4581.1, Mode: "usb", Baud: 50, MarkHz: 1675, SpaceHz: 2125, Expect: "DDK2"}
	now := time.Date(2026, 10, 22, 22, 30, 0, 0, time.UTC)
	entries := []Entry{{ID: "e1", Target: target, StartMinute: 22 * 60, EndMinute: 23 * 60}}
	crossCalls := 0
	r := Runner{
		Dir: dir, Listen: time.Second,
		Receivers: []Receiver{{URL: "test://rx", Type: "websdr", MinKHz: 0, MaxKHz: 30000}},
		Now:       func() time.Time { return now },
		Record: func(ctx context.Context, _ Receiver, _ decode.HFTarget, _ time.Duration) (decode.HFResult, error) {
			return decode.HFResult{
				Text:     "CQ CQ CQ DE DDK2 DDK2 FREQUENCIES 4583 KHZ",
				AudioWAV: fakeWAV(),
			}, nil
		},
		CrossCheck: func(ctx context.Context, _ decode.HFTarget, wavPath, goText, dir, key string) RTTYVerdict {
			crossCalls++
			ref := "CQ CQ CQ DE DDK2 DDK2 FREQUENCIES 4583 KHZ"
			path := dir + "/" + key + ".ref.txt"
			if err := os.WriteFile(path, []byte(ref), 0o600); err != nil {
				t.Fatal(err)
			}
			verified, lines, reason := RTTYAgreement(goText, ref, "DDK2")
			return RTTYVerdict{Checked: true, Engine: "minimodem", RefTextPath: path, Verified: verified, VerifiedLines: lines, Reason: reason}
		},
	}
	a, err := r.RunOnce(context.Background(), entries)
	if err != nil {
		t.Fatal(err)
	}
	if a == nil {
		t.Fatal("no capture")
	}
	if crossCalls != 1 {
		t.Fatalf("cross-check ran %d times, want 1", crossCalls)
	}
	if !a.Verified || a.RefEngine != "minimodem" || a.VerifiedLines == 0 {
		t.Errorf("audit: verified=%v lines=%d engine=%q reason=%q", a.Verified, a.VerifiedLines, a.RefEngine, a.VerifiedReason)
	}
	if a.RefText == "" {
		t.Error("reference text path not recorded")
	}
	if b, err := os.ReadFile(a.RefText); err != nil || !strings.Contains(string(b), "DDK2") {
		t.Errorf("reference text file: %v %q", err, b)
	}
	// Ledger carries the verified fields.
	b, err := os.ReadFile(dir + "/" + LedgerName)
	if err != nil {
		t.Fatal(err)
	}
	var e LedgerEntry
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(b))), &e); err != nil {
		t.Fatal(err)
	}
	if !e.Verified || e.VerifiedLines == 0 || e.RefEngine != "minimodem" || e.RefText == "" {
		t.Errorf("ledger: %+v", e)
	}
}

// TestRunOnceRTTYCrossCheckDisagrees: when the engines disagree the capture
// stays unverified with the reason recorded, and nothing throws.
func TestRunOnceRTTYCrossCheckDisagrees(t *testing.T) {
	dir := t.TempDir()
	target := decode.HFTarget{Name: "DWD 1 Pinneberg RTTY", Kind: "rtty", CarrierKHz: 7646, DialKHz: 7644.1, Mode: "usb", Baud: 50, MarkHz: 1675, SpaceHz: 2125, Expect: "DDH7"}
	now := time.Date(2026, 10, 22, 22, 30, 0, 0, time.UTC)
	entries := []Entry{{ID: "e1", Target: target, StartMinute: 22 * 60, EndMinute: 23 * 60}}
	r := Runner{
		Dir: dir, Listen: time.Second,
		Receivers: []Receiver{{URL: "test://rx", Type: "websdr", MinKHz: 0, MaxKHz: 30000}},
		Now:       func() time.Time { return now },
		Record: func(ctx context.Context, _ Receiver, _ decode.HFTarget, _ time.Duration) (decode.HFResult, error) {
			return decode.HFResult{Text: "CQ CQ CQ DE DDH7 DDH7", AudioWAV: fakeWAV()}, nil
		},
		CrossCheck: func(ctx context.Context, _ decode.HFTarget, _, _, _, _ string) RTTYVerdict {
			return RTTYVerdict{Reason: "reference unavailable: minimodem not installed"}
		},
	}
	a, err := r.RunOnce(context.Background(), entries)
	if err != nil {
		t.Fatal(err)
	}
	if a.Verified {
		t.Error("capture marked verified without a reference run")
	}
	if a.VerifiedReason == "" {
		t.Error("no reason recorded")
	}
}

// TestRunOnceNonRTTYSkipsCrossCheck: NAVTEX and other kinds never invoke
// the RTTY reference path.
func TestRunOnceNonRTTYSkipsCrossCheck(t *testing.T) {
	dir := t.TempDir()
	target := decode.HFTarget{Name: "Niton NAVTEX", Kind: "navtex", CarrierKHz: 518, DialKHz: 516.3, Mode: "usb"}
	now := time.Date(2026, 10, 22, 22, 30, 0, 0, time.UTC)
	entries := []Entry{{ID: "e1", Target: target, StartMinute: 22 * 60, EndMinute: 23 * 60}}
	r := Runner{
		Dir: dir, Listen: time.Second,
		Receivers: []Receiver{{URL: "test://rx", Type: "websdr", MinKHz: 0, MaxKHz: 30000}},
		Now:       func() time.Time { return now },
		Record: func(ctx context.Context, _ Receiver, _ decode.HFTarget, _ time.Duration) (decode.HFResult, error) {
			return decode.HFResult{Text: "ZCZC FA11", AudioWAV: fakeWAV()}, nil
		},
		CrossCheck: func(ctx context.Context, _ decode.HFTarget, _, _, _, _ string) RTTYVerdict {
			t.Error("cross-check ran for non-RTTY target")
			return RTTYVerdict{}
		},
	}
	if _, err := r.RunOnce(context.Background(), entries); err != nil {
		t.Fatal(err)
	}
}
