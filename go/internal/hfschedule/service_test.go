package hfschedule

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/decode"
)

func okRecord(samples int) RecordFunc {
	return func(context.Context, Receiver, decode.HFTarget, time.Duration) (decode.HFResult, error) {
		buf := make([]int16, samples)
		buf[0] = 4
		return decode.HFResult{AudioWAV: decode.WriteWAV16(buf, 12000)}, nil
	}
}

func daylongEntry(id string) Entry {
	return Entry{ID: id, StartMinute: 0, EndMinute: 1440, Target: decode.HFTarget{Name: id, CarrierKHz: 6000, DialKHz: 6000, Mode: "am"}}
}

func readState(t *testing.T, dir string) State {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var st State
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestReceiverMetricsPersisted(t *testing.T) {
	now := utc("2026-10-22T06:00:00Z")
	dir := t.TempDir()
	r := Runner{Dir: dir, Receivers: []Receiver{{URL: "a", Type: "kiwi", MaxKHz: 30000}}, Now: func() time.Time { return now }, Record: okRecord(720000)}
	a, err := r.RunOnce(context.Background(), []Entry{daylongEntry("m1")})
	if err != nil || a.Status != "recorded" {
		t.Fatal(a, err)
	}
	st := readState(t, dir)
	m := st.Metrics["a"]
	if m.Attempts != 1 || m.Successes != 1 || m.Failures != 0 || m.ConsecutiveFailures != 0 {
		t.Fatalf("metrics after success: %+v", m)
	}
	if m.LastSuccess.IsZero() {
		t.Fatal("last success not persisted")
	}

	// Now fail on a different entry and check the streak and error text.
	r.Record = func(context.Context, Receiver, decode.HFTarget, time.Duration) (decode.HFResult, error) {
		return decode.HFResult{}, fmt.Errorf("refused")
	}
	a, err = r.RunOnce(context.Background(), []Entry{daylongEntry("m2")})
	if err != nil || a.Status != "failed" {
		t.Fatal(a, err)
	}
	st = readState(t, dir)
	m = st.Metrics["a"]
	if m.Attempts != 2 || m.Successes != 1 || m.Failures != 1 || m.ConsecutiveFailures != 1 || m.LastError != "refused" {
		t.Fatalf("metrics after failure: %+v", m)
	}
}

func TestSelectReceiverByMetrics(t *testing.T) {
	now := utc("2026-10-22T06:00:00Z")
	rs := []Receiver{
		{URL: "a", Type: "kiwi", MaxKHz: 30000},
		{URL: "b", Type: "kiwi", MaxKHz: 30000},
	}
	// a failed long ago (cooldown expired) and has a poor record; b is clean.
	metrics := map[string]ReceiverMetrics{
		"a": {Attempts: 4, Successes: 1, Failures: 3, ConsecutiveFailures: 3, LastFailure: now.Add(-25 * time.Hour), LastError: "timeout"},
		"b": {Attempts: 1, Successes: 1, LastSuccess: now.Add(-time.Hour)},
	}
	got, ok := selectReceiver(rs, metrics, now)
	if !ok || got.URL != "b" {
		t.Fatalf("want b, got %v", got)
	}

	// Both benched: no receiver.
	benched := map[string]ReceiverMetrics{
		"a": {ConsecutiveFailures: 1, LastFailure: now.Add(-time.Minute)},
		"b": {ConsecutiveFailures: 2, LastFailure: now.Add(-time.Minute)},
	}
	if _, ok := selectReceiver(rs, benched, now); ok {
		t.Fatal("all receivers benched, none should be picked")
	}
	// After a's 1h cooldown expires but b's 2h has not, a returns.
	if got, ok := selectReceiver(rs, benched, now.Add(61*time.Minute)); !ok || got.URL != "a" {
		t.Fatalf("want a back after its cooldown, got %v ok=%v", got, ok)
	}
}

func TestCooldownDoubling(t *testing.T) {
	m := ReceiverMetrics{}
	if m.cooldown() != 0 {
		t.Fatal("no failures, no cooldown")
	}
	m.ConsecutiveFailures = 1
	if m.cooldown() != time.Hour {
		t.Fatalf("first failure: %s", m.cooldown())
	}
	m.ConsecutiveFailures = 3
	if m.cooldown() != 4*time.Hour {
		t.Fatalf("third failure: %s", m.cooldown())
	}
	m.ConsecutiveFailures = 40
	if m.cooldown() != 24*time.Hour {
		t.Fatalf("cap: %s", m.cooldown())
	}
}

func TestServiceRunsAndReloads(t *testing.T) {
	now := utc("2026-10-22T06:00:00Z")
	var loads, audits atomic.Int64
	r := &Runner{Dir: t.TempDir(), Receivers: []Receiver{{URL: "a", Type: "kiwi", MaxKHz: 30000}}, Now: func() time.Time { return now }, Record: okRecord(720000)}
	svc := Service{
		Runner: r,
		Load: func(context.Context) ([]Entry, error) {
			loads.Add(1)
			return []Entry{daylongEntry(fmt.Sprintf("e%d", loads.Load()))}, nil
		},
		PollInterval:   20 * time.Millisecond,
		ReloadInterval: 55 * time.Millisecond,
		OnAudit:        func(Audit) { audits.Add(1) },
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := svc.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if audits.Load() < 2 {
		t.Fatalf("expected repeated captures from the service loop, got %d", audits.Load())
	}
	if loads.Load() < 2 {
		t.Fatalf("expected at least one schedule reload, got %d", loads.Load())
	}
}

func TestServiceKeepsScheduleOnReloadFailure(t *testing.T) {
	now := utc("2026-10-22T06:00:00Z")
	var loads atomic.Int64
	var audits atomic.Int64
	r := &Runner{Dir: t.TempDir(), Receivers: []Receiver{{URL: "a", Type: "kiwi", MaxKHz: 30000}}, Now: func() time.Time { return now }, Record: okRecord(720000)}
	svc := Service{
		Runner: r,
		Load: func(context.Context) ([]Entry, error) {
			n := loads.Add(1)
			if n > 1 {
				return nil, fmt.Errorf("source down")
			}
			return []Entry{daylongEntry(fmt.Sprintf("e%d", n))}, nil
		},
		PollInterval:   20 * time.Millisecond,
		ReloadInterval: 40 * time.Millisecond,
		OnAudit:        func(Audit) { audits.Add(1) },
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	// A failed reload must not kill the service.
	if err := svc.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if audits.Load() == 0 {
		t.Fatal("no captures after reload failure")
	}
}

func TestLocalCancelDoesNotBenchReceiver(t *testing.T) {
	// A capture cancelled by our own shutdown (parent context done) is not
	// a receiver fault: it must not grow the failure streak, must not
	// bench, and is recorded as interrupted with class local_cancel.
	dir := t.TempDir()
	r := Runner{
		Dir:       dir,
		Receivers: []Receiver{{URL: "a", Type: "kiwi", MaxKHz: 30000}},
		Now:       func() time.Time { return utc("2026-10-22T06:00:00Z") },
		Record: func(context.Context, Receiver, decode.HFTarget, time.Duration) (decode.HFResult, error) {
			return decode.HFResult{}, context.Canceled
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already done: every RunOnce record sees a dead parent ctx
	a, err := r.RunOnce(ctx, []Entry{daylongEntry("c1")})
	if err != nil || a == nil {
		t.Fatal(a, err)
	}
	if a.Status != "interrupted" || a.FailureClass != "local_cancel" {
		t.Fatalf("want interrupted/local_cancel, got %s/%s", a.Status, a.FailureClass)
	}
	m := readState(t, dir).Metrics["a"]
	if m.Attempts != 0 {
		t.Fatalf("local cancel must not touch receiver metrics: %+v", m)
	}
}

func TestSlotEndDeadlineDoesNotBenchReceiver(t *testing.T) {
	// A capture cut by its planned slot-end deadline delivered for the
	// whole window: class slot_end, no bench, no metrics penalty.
	dir := t.TempDir()
	r := Runner{
		Dir:       dir,
		Receivers: []Receiver{{URL: "a", Type: "kiwi", MaxKHz: 30000}},
		Now:       func() time.Time { return utc("2026-10-22T06:00:00Z") },
		Record: func(context.Context, Receiver, decode.HFTarget, time.Duration) (decode.HFResult, error) {
			return decode.HFResult{}, context.DeadlineExceeded
		},
	}
	a, err := r.RunOnce(context.Background(), []Entry{daylongEntry("s1")})
	if err != nil || a == nil {
		t.Fatal(a, err)
	}
	if a.Status != "failed" || a.FailureClass != "slot_end" {
		t.Fatalf("want failed/slot_end, got %s/%s", a.Status, a.FailureClass)
	}
	m := readState(t, dir).Metrics["a"]
	if m.Attempts != 0 {
		t.Fatalf("slot-end cut must not touch receiver metrics: %+v", m)
	}
}

func TestReceiverIOFaultStillBenches(t *testing.T) {
	// A genuine stream/read failure remains a receiver fault: class
	// receiver_io, streak grows, cooldown applies.
	dir := t.TempDir()
	r := Runner{
		Dir:       dir,
		Receivers: []Receiver{{URL: "a", Type: "kiwi", MaxKHz: 30000}},
		Now:       func() time.Time { return utc("2026-10-22T06:00:00Z") },
		Record: func(context.Context, Receiver, decode.HFTarget, time.Duration) (decode.HFResult, error) {
			return decode.HFResult{}, fmt.Errorf("read tcp: i/o timeout")
		},
	}
	a, err := r.RunOnce(context.Background(), []Entry{daylongEntry("f1")})
	if err != nil || a == nil {
		t.Fatal(a, err)
	}
	if a.Status != "failed" || a.FailureClass != "receiver_io" {
		t.Fatalf("want failed/receiver_io, got %s/%s", a.Status, a.FailureClass)
	}
	m := readState(t, dir).Metrics["a"]
	if m.Attempts != 1 || m.ConsecutiveFailures != 1 {
		t.Fatalf("io fault must count: %+v", m)
	}
}

func TestWatchdogCancelDoesNotBenchReceiver(t *testing.T) {
	// After a host suspend the wall-clock watchdog cancels the capture
	// context, so Record returns context.Canceled while the outer
	// service context stays alive. That is our own wall-cap acting,
	// not receiver evidence: class slot_end, no bench, no metrics.
	dir := t.TempDir()
	r := Runner{
		Dir:       dir,
		Receivers: []Receiver{{URL: "a", Type: "kiwi", MaxKHz: 30000}},
		Now:       func() time.Time { return utc("2026-10-22T06:00:00Z") },
		Record: func(ctx context.Context, _ Receiver, _ decode.HFTarget, _ time.Duration) (decode.HFResult, error) {
			return decode.HFResult{}, context.Canceled
		},
	}
	a, err := r.RunOnce(context.Background(), []Entry{daylongEntry("w1")})
	if err != nil || a == nil {
		t.Fatal(a, err)
	}
	if a.Status != "failed" || a.FailureClass != "slot_end" {
		t.Fatalf("watchdog cancel must classify as slot_end, got %s/%s", a.Status, a.FailureClass)
	}
	m := readState(t, dir).Metrics["a"]
	if m.Attempts != 0 {
		t.Fatalf("watchdog cancel must not touch receiver metrics: %+v", m)
	}
}
