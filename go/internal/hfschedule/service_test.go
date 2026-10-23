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

func TestUpcomingSlotProtectedFromRetry(t *testing.T) {
	// A failed occurrence with a long window becomes retry-eligible. Its
	// retry must not start when a distinct scheduled slot begins inside the
	// retry's planned capture window: the retry defers, the slot captures
	// on time, and the retry runs afterwards.
	now := utc("2026-10-22T00:39:30Z")
	dir := t.TempDir()
	retry := Entry{ID: "retry", StartMinute: 5, EndMinute: 55, Target: decode.HFTarget{Name: "retry", CarrierKHz: 6000, DialKHz: 6000, Mode: "usb"}}
	slot := Entry{ID: "slot", StartMinute: 40, EndMinute: 50, Target: decode.HFTarget{Name: "slot", CarrierKHz: 7000, DialKHz: 7000, Mode: "usb"}}
	r := Runner{
		Dir:       dir,
		Receivers: []Receiver{{URL: "a", Type: "kiwi", MaxKHz: 30000}, {URL: "b", Type: "kiwi", MaxKHz: 30000}},
		Listen:    240 * time.Second,
		Now:       func() time.Time { return now },
		Record:    okRecord(2880000),
	}
	// Seed a past failure on the retry entry so it is retry-eligible (this
	// benches receiver a, so later captures in this test land on b).
	r.Record = func(context.Context, Receiver, decode.HFTarget, time.Duration) (decode.HFResult, error) {
		return decode.HFResult{}, fmt.Errorf("refused")
	}
	failAt := utc("2026-10-22T00:10:00Z")
	r.Now = func() time.Time { return failAt }
	a, err := r.RunOnce(context.Background(), []Entry{retry})
	if err != nil || a.Status != "failed" {
		t.Fatal(a, err)
	}
	// At 00:39:30 the retry would run 240s and overlap the 00:40 slot.
	r.Now = func() time.Time { return now }
	var captured []string
	r.Record = func(_ context.Context, _ Receiver, tg decode.HFTarget, d time.Duration) (decode.HFResult, error) {
		captured = append(captured, tg.Name)
		return okRecord(2880000)(context.Background(), Receiver{}, tg, d)
	}
	a, err = r.RunOnce(context.Background(), []Entry{retry, slot})
	if err != nil {
		t.Fatal(err)
	}
	if a != nil || len(captured) != 0 {
		t.Fatalf("retry should defer before the upcoming slot, got audit %+v captures %v", a, captured)
	}
	// Once the slot starts, it is captured on time.
	slotNow := utc("2026-10-22T00:40:00Z")
	r.Now = func() time.Time { return slotNow }
	a, err = r.RunOnce(context.Background(), []Entry{retry, slot})
	if err != nil || a == nil || a.Occurrence.Entry.ID != "slot" {
		t.Fatalf("slot should capture at its start, got %+v err %v", a, err)
	}
	// After the slot is recorded, the retry proceeds with its window intact.
	after := utc("2026-10-22T00:44:10Z")
	r.Now = func() time.Time { return after }
	a, err = r.RunOnce(context.Background(), []Entry{retry, slot})
	if err != nil || a == nil || a.Occurrence.Entry.ID != "retry" {
		t.Fatalf("retry should resume after the slot, got %+v err %v", a, err)
	}
}
