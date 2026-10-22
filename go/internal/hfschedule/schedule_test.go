package hfschedule

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/decode"
)

func utc(s string) time.Time {
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return v
}
func row(freq, period, days, name, lang string) string {
	return fmt.Sprintf("%-14s%-9s %-5s %-3s %-23s %-5s %-4s", freq, period, days, "D", name, lang, "Eu")
}
func TestEiBi(t *testing.T) {
	text := "Valid March 29, 2026 - October 25, 2026\n" + row("7646", "0000-2400", "", "DWD 1 Pinneberg RTTY", "E") + "\n" + row("6314", "0140-0150", "135", "NMF NAVTEX Boston MA", "") + "\n" + row("6000", "2300-0100", "Th", "Radio test", "E") + "\n" + row("17.2", "1230-1315", "24Oct", "SAQ Grimeton", "-CW")
	entries, err := ParseEiBi(strings.NewReader(text))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("entries %+v", entries)
	}
	if entries[0].Target.Expect != "DDH7" || entries[0].Target.DialKHz != 7644.1 {
		t.Fatal(entries[0])
	}
	if entries[1].Target.Kind != "navtex" || entries[1].Target.DialKHz != 6312.3 {
		t.Fatal(entries[1])
	}
	got := Plan(entries, utc("2026-10-23T00:30:00Z"), 0)
	if len(got) != 2 {
		t.Fatalf("midnight plan %+v", got)
	}
	if got[0].Entry.Target.Name != "Radio test" || got[0].Start != utc("2026-10-22T23:00:00Z") {
		t.Fatal(got)
	}
	if len(Plan(entries, utc("2026-10-26T12:00:00Z"), time.Hour)) != 0 {
		t.Fatal("expired schedule used")
	}
	for _, code := range []string{"irr", "altFr", "tent", "1.Sa", "Last7"} {
		if dayMatches(code, utc("2026-10-22T00:00:00Z")) {
			t.Fatal(code)
		}
	}
	if !dayMatches("24Oct", utc("2026-10-24T00:00:00Z")) || dayMatches("135", utc("2026-10-22T00:00:00Z")) {
		t.Fatal("days")
	}
	for _, bad := range []string{"2460", "2500", "-100", "123"} {
		if _, err := minute(bad, true); err == nil {
			t.Fatal(bad)
		}
	}
	if _, err := ParseEiBi(strings.NewReader("<html>captcha</html>")); err == nil {
		t.Fatal("accepted HTML")
	}
}
func TestNOAA(t *testing.T) {
	s := `Valid 19 September 2018
BOSTON, MASSACHUSETTS, U.S.A.
NMF            4235   kHz     0230z-1039           F3C        4 KW
               6340.5 kHz     ALL BROADCAST TIMES  F3C        4 KW
              12750   kHz     1400z-2239           F3C        4 KW
0230/1400  TEST PATTERN                             120/576
0233/----  PRELIMINARY SURFACE ANALYSIS             120/576   0000    1
0243/1405  BROADCAST SCHEDULE (PART 1)              120/576
`
	entries, err := ParseNOAA(strings.NewReader(s))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 10 {
		t.Fatalf("%d %+v", len(entries), entries)
	}
	if entries[0].EndMinute != 153 || entries[0].Target.DialKHz != 4233.1 {
		t.Fatal(entries[0])
	}
	active := Plan(entries, utc("2026-10-22T14:02:00Z"), 0)
	if len(active) != 2 {
		t.Fatal(active)
	}
	for _, e := range active {
		if e.Entry.Target.CarrierKHz == 4235 {
			t.Fatal("night frequency used in daylight")
		}
	}
}
func TestRunner(t *testing.T) {
	now := utc("2026-10-22T06:00:00Z")
	dir := t.TempDir()
	calls := 0
	target := decode.HFTarget{Name: "Test broadcast", Kind: "broadcast", CarrierKHz: 6000, DialKHz: 6000, Mode: "am"}
	entry := Entry{ID: "test", Target: target, StartMinute: 360, EndMinute: 361}
	rs := []Receiver{{URL: "fake", Type: "kiwi", MinKHz: 100, MaxKHz: 30000}}
	runner := Runner{Dir: dir, Receivers: rs, Listen: 90 * time.Second, Now: func() time.Time { return now }, Record: func(_ context.Context, _ Receiver, _ decode.HFTarget, d time.Duration) (decode.HFResult, error) {
		calls++
		if d != time.Minute {
			t.Errorf("not end-bounded: %s", d)
		}
		samples := make([]int16, 720000)
		samples[0] = 4
		return decode.HFResult{AudioWAV: decode.WriteWAV16(samples, 12000)}, nil
	}}
	a, err := runner.RunOnce(context.Background(), []Entry{entry})
	if err != nil {
		t.Fatal(err)
	}
	if a.Status != "recorded" || a.DecodeLive || a.Peak != 4 || a.SampleRate != 12000 {
		t.Fatal(a)
	}
	if _, err := os.Stat(a.WAV); err != nil {
		t.Fatal(err)
	}
	if a, err = runner.RunOnce(context.Background(), []Entry{entry}); err != nil || a != nil || calls != 1 {
		t.Fatal(a, err, calls)
	}
	b, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var state State
	if err = json.Unmarshal(b, &state); err != nil {
		t.Fatal(err)
	}
	// Persisted in-flight work is never duplicated after restart.
	state.Attempts["test-20261022T0600Z"] = Audit{Occurrence: Occurrence{Entry: entry, Start: now, End: now.Add(time.Minute)}, Status: "interrupted"}
	if err = saveJSON(filepath.Join(dir, "state.json"), state); err != nil {
		t.Fatal(err)
	}
	if a, err = runner.RunOnce(context.Background(), []Entry{entry}); err != nil || a != nil {
		t.Fatal(a, err)
	}
	if err = os.WriteFile(filepath.Join(dir, "state.json"), []byte("bad"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = runner.RunOnce(context.Background(), []Entry{entry}); err == nil {
		t.Fatal("corrupt state ignored")
	}
}
func TestReceiverSelectionAndLock(t *testing.T) {
	target := decode.HFTarget{DialKHz: 516.3, CarrierKHz: 518}
	rs := []Receiver{{URL: "x", Type: "kiwi", MinKHz: 520, MaxKHz: 30000}, {URL: "z", Type: "websdr", MaxKHz: 30000}, {URL: "a", Type: "kiwi", MaxKHz: 30000}}
	got := ReceiversFor(rs, target)
	if len(got) != 2 || got[0].URL != "a" {
		t.Fatal(got)
	}
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "scheduler.lock"), 0700); err != nil {
		t.Fatal(err)
	}
	r := Runner{Dir: dir}
	if _, err := r.RunOnce(context.Background(), nil); err == nil {
		t.Fatal("ignored lock")
	}
}
func TestRunnerFailuresAreNotSuccess(t *testing.T) {
	now := utc("2026-10-22T06:00:00Z")
	calls := 0
	r := Runner{Dir: t.TempDir(), Receivers: []Receiver{{URL: "x", Type: "kiwi", MaxKHz: 30000}}, Now: func() time.Time { return now }, Record: func(context.Context, Receiver, decode.HFTarget, time.Duration) (decode.HFResult, error) {
		calls++
		return decode.HFResult{}, fmt.Errorf("refused")
	}}
	entry := Entry{ID: "x", StartMinute: 0, EndMinute: 1440, Target: decode.HFTarget{CarrierKHz: 6000, DialKHz: 6000}}
	a, err := r.RunOnce(context.Background(), []Entry{entry})
	if err != nil || a.Status != "failed" || a.DecodeLive {
		t.Fatal(a, err)
	}
	a, err = r.RunOnce(context.Background(), []Entry{entry})
	if err != nil || a != nil || calls != 1 {
		t.Fatal(a, err, calls)
	}
}

func TestExactStartEndAndUTC(t *testing.T) {
	e := Entry{ID: "daily", Days: "Th", StartMinute: 360, EndMinute: 370}
	if len(Plan([]Entry{e}, utc("2026-10-22T05:59:59Z"), 0)) != 0 {
		t.Fatal("started early")
	}
	if len(Plan([]Entry{e}, utc("2026-10-22T06:00:00Z"), 0)) != 1 {
		t.Fatal("missed start")
	}
	if len(Plan([]Entry{e}, utc("2026-10-22T06:10:00Z"), 0)) != 0 {
		t.Fatal("end is inclusive")
	}
	if len(Plan([]Entry{e}, utc("2026-10-22T09:05:00+03:00"), 0)) != 1 {
		t.Fatal("local time leaked into UTC scheduling")
	}
}

func TestShortCaptureRejected(t *testing.T) {
	now := utc("2026-10-22T06:00:00Z")
	r := Runner{Dir: t.TempDir(), Receivers: []Receiver{{URL: "x", Type: "kiwi", MaxKHz: 30000}}, Now: func() time.Time { return now }, Record: func(context.Context, Receiver, decode.HFTarget, time.Duration) (decode.HFResult, error) {
		return decode.HFResult{AudioWAV: decode.WriteWAV16([]int16{1, 2, 3}, 12000)}, nil
	}}
	entry := Entry{ID: "short", StartMinute: 0, EndMinute: 1440, Target: decode.HFTarget{CarrierKHz: 6000, DialKHz: 6000}}
	a, err := r.RunOnce(context.Background(), []Entry{entry})
	if err != nil || a.Status != "failed" || a.DecodeLive || a.WAV == "" {
		t.Fatal(a, err)
	}
}
func TestFallbackAfterHostRefusal(t *testing.T) {
	now := utc("2026-10-22T06:00:00Z")
	var used []string
	r := Runner{Dir: t.TempDir(), Receivers: []Receiver{{URL: "a", Type: "kiwi", MaxKHz: 30000}, {URL: "b", Type: "kiwi", MaxKHz: 30000}}, Now: func() time.Time { return now }, Record: func(_ context.Context, rx Receiver, _ decode.HFTarget, _ time.Duration) (decode.HFResult, error) {
		used = append(used, rx.URL)
		return decode.HFResult{}, fmt.Errorf("refused")
	}}
	entry := Entry{ID: "retry", StartMinute: 0, EndMinute: 1440, Target: decode.HFTarget{CarrierKHz: 6000, DialKHz: 6000}}
	if _, err := r.RunOnce(context.Background(), []Entry{entry}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	if _, err := r.RunOnce(context.Background(), []Entry{entry}); err != nil {
		t.Fatal(err)
	}
	if len(used) != 2 || used[0] != "a" || used[1] != "b" {
		t.Fatal(used)
	}
}
