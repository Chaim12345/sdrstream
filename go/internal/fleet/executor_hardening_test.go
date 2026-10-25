package fleet

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/monitor"
)

func TestExecutorRecoversPanicAndRecordsFailure(t *testing.T) {
	p := NewPlanner()
	fo := newFakeOpen()
	ex := &Executor{Planner: p, Open: fo.open, Work: func(ctx context.Context, a Assignment, src monitor.Source) error {
		panic("decoder blew up")
	}}
	ex.nowFunc = func() time.Time { return testNow }
	first := ex.RunOnce(context.Background(), fixture(), []Job{ft8job}, testNow) // must not panic
	failed := first.Assigned[0].Station.Key
	if *fo.closedCnt != 1 {
		t.Fatalf("panicked work left the source open (closed %d)", *fo.closedCnt)
	}
	second := p.Plan(fixture(), []Job{ft8job}, testNow.Add(time.Minute))
	if second.Assigned[0].Station.Key == failed {
		t.Fatalf("job re-assigned to %s after a panic", failed)
	}
}

func TestExecutorCancellationIsNeutral(t *testing.T) {
	p := NewPlanner()
	fo := newFakeOpen()
	ex := &Executor{Planner: p, Open: fo.open, Work: okWork}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled: a shutdown is not a station failure
	ex.RunOnce(ctx, fixture(), []Job{ft8job}, testNow)
	if p.cooldownSize() != 0 {
		t.Fatalf("cancellation parked a pair: cooldown size %d", p.cooldownSize())
	}
}

func TestExecutorNilDepsAndNilSource(t *testing.T) {
	p := NewPlanner()
	fo := newFakeOpen()
	// nil Work: must not panic, outcome is failure.
	ex := &Executor{Planner: p, Open: fo.open}
	ex.RunOnce(context.Background(), fixture(), []Job{ft8job}, testNow)
	if p.cooldownSize() != 1 {
		t.Fatalf("nil Work should record a failure, cooldown %d", p.cooldownSize())
	}
	// Open returning (nil, nil): must not panic on Close, outcome failure.
	p2 := NewPlanner()
	ex2 := &Executor{Planner: p2, Open: func(ctx context.Context, a Assignment) (monitor.Source, error) {
		return nil, nil
	}, Work: okWork}
	ex2.RunOnce(context.Background(), fixture(), []Job{ft8job}, testNow)
	if p2.cooldownSize() != 1 {
		t.Fatalf("nil source should record a failure, cooldown %d", p2.cooldownSize())
	}
	// nil Open: same.
	p3 := NewPlanner()
	ex3 := &Executor{Planner: p3, Work: okWork}
	ex3.RunOnce(context.Background(), fixture(), []Job{ft8job}, testNow)
	if p3.cooldownSize() != 1 {
		t.Fatalf("nil Open should record a failure, cooldown %d", p3.cooldownSize())
	}
}

func TestExecutorRecordsOutcomeAtCompletionTime(t *testing.T) {
	p := NewPlanner()
	fo := newFakeOpen()
	ex := &Executor{Planner: p, Open: fo.open, Work: func(ctx context.Context, a Assignment, src monitor.Source) error {
		return errors.New("decode crashed")
	}}
	planTime := testNow
	completionTime := testNow.Add(45 * time.Minute)
	ex.nowFunc = func() time.Time { return completionTime } // injected clock
	ex.RunOnce(context.Background(), fixture(), []Job{ft8job}, planTime)
	// If the outcome were recorded at plan time, the 30min cooldown would
	// expire by planTime+31m. Recorded at completion time, the pair is
	// still parked then and only frees at completionTime+30m.
	mid := p.Plan(fixture(), []Job{ft8job}, planTime.Add(31*time.Minute))
	if mid.Assigned[0].Station.Key == "hf-b" {
		t.Fatalf("outcome appears recorded at plan time, not completion time")
	}
	late := p.Plan(fixture(), []Job{ft8job}, completionTime.Add(31*time.Minute))
	// (Both other stations also failed? No - only hf-b was parked; either
	// way by completion+31m the hf-b pair is eligible again.)
	if len(late.Assigned) != 1 {
		t.Fatalf("late plan: %+v", late)
	}
}

func TestExecutorSurvivesPanickingClose(t *testing.T) {
	p := NewPlanner()
	ex := &Executor{Planner: p, Open: func(ctx context.Context, a Assignment) (monitor.Source, error) {
		return &panicCloseSource{}, nil
	}, Work: okWork}
	ex.RunOnce(context.Background(), fixture(), []Job{ft8job}, testNow) // must not panic
}

type panicCloseSource struct{ fakeSource }

func (s *panicCloseSource) Close() error { panic("close blew up") }
