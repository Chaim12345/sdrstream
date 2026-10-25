package fleet

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/monitor"
	"github.com/chaim12345/sdrstream/go/internal/probe"
)

// fakeSource satisfies monitor.Source without any network.
type fakeSource struct{ closed *int }

func (s *fakeSource) Spectrum() ([]float64, float64, float64, bool) { return nil, 0, 0, false }
func (s *fakeSource) Listen(ctx context.Context, hz float64, mode string, dur time.Duration) ([]float64, int, error) {
	return nil, 0, nil
}
func (s *fakeSource) Close() error { *s.closed++; return nil }

type fakeOpen struct {
	mu        sync.Mutex
	calls     []string        // station keys opened
	failOn    map[string]bool // station keys whose open fails
	inflight  int
	maxSeen   int
	closedCnt *int
}

func (f *fakeOpen) open(ctx context.Context, a Assignment) (monitor.Source, error) {
	f.mu.Lock()
	f.calls = append(f.calls, a.Station.Key)
	f.inflight++
	if f.inflight > f.maxSeen {
		f.maxSeen = f.inflight
	}
	f.inflight--
	fail := f.failOn[a.Station.Key]
	f.mu.Unlock()
	if fail {
		return nil, errors.New("dial refused")
	}
	return &fakeSource{closed: f.closedCnt}, nil
}

func newFakeOpen() *fakeOpen {
	return &fakeOpen{failOn: map[string]bool{}, closedCnt: new(int)}
}

func okWork(ctx context.Context, a Assignment, src monitor.Source) error { return nil }

func TestExecutorRunsAssignmentsClosesSources(t *testing.T) {
	p := NewPlanner()
	fo := newFakeOpen()
	ex := &Executor{Planner: p, Open: fo.open, Work: okWork}
	plan := ex.RunOnce(context.Background(), fixture(), []Job{ft8job}, testNow)
	if len(plan.Assigned) != 1 {
		t.Fatalf("plan: %+v", plan)
	}
	if len(fo.calls) != 1 || fo.calls[0] != plan.Assigned[0].Station.Key {
		t.Fatalf("opens: %v", fo.calls)
	}
	if *fo.closedCnt != 1 {
		t.Fatalf("source closed %d times, want 1", *fo.closedCnt)
	}
	// Success keeps the pair out of cooldown: next plan sticks.
	next := p.Plan(fixture(), []Job{ft8job}, testNow.Add(time.Minute))
	if next.Assigned[0].Station.Key != plan.Assigned[0].Station.Key {
		t.Fatalf("successful pair should stick: %s", next.Assigned[0].Station.Key)
	}
}

func TestExecutorOpenFailureParksPair(t *testing.T) {
	p := NewPlanner()
	fo := newFakeOpen()
	// The best-ranked station's dial fails: the executor must record the
	// failure so the next plan moves the job.
	fo.failOn["hf-b"] = true
	ex := &Executor{Planner: p, Open: fo.open, Work: okWork}
	ex.nowFunc = func() time.Time { return testNow }
	first := ex.RunOnce(context.Background(), fixture(), []Job{ft8job}, testNow)
	failed := first.Assigned[0].Station.Key
	if failed != "hf-b" {
		t.Fatalf("setup: first plan took %s, want hf-b", failed)
	}
	second := p.Plan(fixture(), []Job{ft8job}, testNow.Add(time.Minute))
	if len(second.Assigned) == 0 {
		t.Fatalf("expected fallback station, unplaced: %+v", second.Unplaced)
	}
	if second.Assigned[0].Station.Key == failed {
		t.Fatalf("job re-assigned to %s right after open failure", failed)
	}
}

func TestExecutorWorkFailureParksPair(t *testing.T) {
	p := NewPlanner()
	fo := newFakeOpen()
	ex := &Executor{Planner: p, Open: fo.open, Work: func(ctx context.Context, a Assignment, src monitor.Source) error {
		return errors.New("decode crashed")
	}}
	ex.nowFunc = func() time.Time { return testNow }
	first := ex.RunOnce(context.Background(), fixture(), []Job{ft8job}, testNow)
	failed := first.Assigned[0].Station.Key
	second := p.Plan(fixture(), []Job{ft8job}, testNow.Add(time.Minute))
	if second.Assigned[0].Station.Key == failed {
		t.Fatalf("job re-assigned to %s right after work failure", failed)
	}
}

func TestExecutorBoundsConcurrency(t *testing.T) {
	p := NewPlanner()
	p.MaxPerStation = 2
	fo := newFakeOpen()
	ex := &Executor{Planner: p, Open: fo.open, Work: okWork, MaxConcurrent: 1}
	jobs := []Job{ft8job, {ID: "ft8-40m", FreqHz: 7_074_000, Mode: "usb", MaxAge: 2 * time.Hour}}
	plan := ex.RunOnce(context.Background(), fixture(), jobs, testNow)
	if len(plan.Assigned) != 2 {
		t.Fatalf("plan: %+v", plan)
	}
	if fo.maxSeen != 1 {
		t.Fatalf("max concurrent opens = %d, want 1", fo.maxSeen)
	}
}

func TestExecutorNeverOpensUnplacedJobs(t *testing.T) {
	p := NewPlanner()
	fo := newFakeOpen()
	ex := &Executor{Planner: p, Open: fo.open, Work: okWork}
	plan := ex.RunOnce(context.Background(), fixture(), []Job{
		{ID: "fm-70cm", FreqHz: 433_500_000, Mode: "nfm", MaxAge: 2 * time.Hour},
	}, testNow)
	if len(plan.Unplaced) != 1 {
		t.Fatalf("plan: %+v", plan)
	}
	if len(fo.calls) != 0 {
		t.Fatalf("opened stations for an unplaced job: %v", fo.calls)
	}
}

func TestMonitorOpenerRejectsUnknownKind(t *testing.T) {
	_, err := MonitorOpener(context.Background(), Assignment{
		Station: probe.Record{Key: "x", Kind: "smoke-detector"},
	})
	if err == nil {
		t.Fatal("unknown kind must error before any dial")
	}
}
