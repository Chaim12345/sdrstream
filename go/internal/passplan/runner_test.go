package passplan

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRunDueLifecycle(t *testing.T) {
	f := fake{map[int][]Pass{1: {pass(60, 10, 45)}}}
	p := newP(f, "")
	p.Generate([]Monitored{mon("a", 1, "", 0)})
	calls := 0
	fn := func(ctx context.Context, o Observation) error { calls++; return nil }

	// not due yet (starts ~61 min out)
	if o, err := p.RunDue(context.Background(), 2*time.Minute, fn); o != nil || err != nil || calls != 0 {
		t.Fatalf("ran early: %v %v %d", o, err, calls)
	}
	// inside the lead window
	p.Now = func() time.Time { return t0.Add(59 * time.Minute) }
	o, err := p.RunDue(context.Background(), 2*time.Minute, fn)
	if err != nil || o == nil || calls != 1 {
		t.Fatalf("not run: %v %v %d", o, err, calls)
	}
	if st := p.Timeline()[0].Status; st != StatusCompleted {
		t.Fatalf("status %s", st)
	}
	// never twice
	if o, _ := p.RunDue(context.Background(), 2*time.Minute, fn); o != nil || calls != 1 {
		t.Fatal("ran a completed observation again")
	}
}

func TestRunDueErrorPanicAndCancel(t *testing.T) {
	cases := map[string]RunFunc{
		"error": func(context.Context, Observation) error { return errors.New("boom") },
		"panic": func(context.Context, Observation) error { panic("kaput") },
		"cancel": func(ctx context.Context, o Observation) error {
			return ctx.Err()
		},
	}
	for name, fn := range cases {
		f := fake{map[int][]Pass{1: {pass(60, 10, 45)}}}
		p := newP(f, "")
		p.Generate([]Monitored{mon("a", 1, "", 0)})
		p.Now = func() time.Time { return t0.Add(61 * time.Minute) }
		ctx, cancel := context.WithCancel(context.Background())
		if name == "cancel" {
			cancel()
		}
		o, err := p.RunDue(ctx, 0, fn)
		cancel()
		tl := p.Timeline()[0]
		if o == nil || err == nil || tl.Status != StatusFailed || tl.Detail == "" {
			t.Fatalf("%s: o=%v err=%v tl=%+v", name, o, err, tl)
		}
	}
}

func TestRunDueSkipsEndedWindows(t *testing.T) {
	f := fake{map[int][]Pass{1: {pass(60, 10, 45)}}}
	p := newP(f, "")
	p.Generate([]Monitored{mon("a", 1, "", 0)})
	p.Now = func() time.Time { return t0.Add(3 * time.Hour) } // window long over
	if o, _ := p.RunDue(context.Background(), time.Hour, func(context.Context, Observation) error { t.Fatal("ran stale"); return nil }); o != nil {
		t.Fatal("ran stale observation")
	}
}
