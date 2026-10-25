package fleet

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/monitor"
	"github.com/chaim12345/sdrstream/go/internal/probe"
)

// Opener dials an assigned station and returns its monitor source. This
// is the ONLY network-touching seam in the fleet package; it is injected
// so everything around it runs offline in tests. MonitorOpener is the
// production opener - it dials live receivers and is gated on the owner's
// approval of live polling.
type Opener func(ctx context.Context, a Assignment) (monitor.Source, error)

// Work is the decode/monitor work run against an opened source (the
// "decode" leg: FT8/RTTY/SSTV decoding, voice monitoring, ...).
type Work func(ctx context.Context, a Assignment, src monitor.Source) error

// Executor runs a planning pass and executes the resulting assignments
// with bounded concurrency, feeding every outcome back into the planner
// (so failed (job, station) pairs cool down and later plans move on).
type Executor struct {
	Planner *Planner
	Open    Opener
	Work    Work
	// MaxConcurrent bounds simultaneously open receivers. Default 2:
	// every assignment holds a scarce public receiver slot.
	MaxConcurrent int

	// nowFunc stamps outcomes at COMPLETION time (test override). Nil =
	// time.Now. QA: cooldown must start when the attempt finished, not
	// when the plan was made, or long work silently shortens the park.
	nowFunc func() time.Time
}

// RunOnce plans jobs against the snapshot and executes each assignment:
// open the station's source, run the work, close the source (always),
// record the outcome at completion time. Sources are closed even when
// open succeeded but work failed or panicked; a half-run assignment
// never leaks a receiver slot.
//
// RunOnce BLOCKS until every assignment's work returns. Long-running
// decodes therefore block the planning loop: campaign designers must
// slice work into bounded pieces (or accept the cadence) rather than
// handing RunOnce an unbounded listen.
func (e *Executor) RunOnce(ctx context.Context, snap probe.Snapshot, jobs []Job, now time.Time) Plan {
	if e.Planner == nil {
		return Plan{Unplaced: append([]Job(nil), jobs...)}
	}
	nowFn := e.nowFunc
	if nowFn == nil {
		nowFn = time.Now
	}
	plan := e.Planner.Plan(snap, jobs, now)
	conc := e.MaxConcurrent
	if conc <= 0 {
		conc = 2
	}
	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	for _, a := range plan.Assigned {
		wg.Add(1)
		sem <- struct{}{}
		go func(a Assignment) {
			defer wg.Done()
			defer func() { <-sem }()
			var src monitor.Source
			ok := false
			// A panic in Open/Work must not kill the engine: recover,
			// close whatever opened, record the failure (QA review).
			func() {
				defer func() { _ = recover() }()
				if ctx.Err() != nil {
					return // shutdown before we even open
				}
				if e.Open == nil || e.Work == nil {
					return // misconfigured executor: counts as failure below
				}
				var err error
				src, err = e.Open(ctx, a)
				if err != nil || src == nil {
					return
				}
				err = e.Work(ctx, a, src)
				ok = err == nil
			}()
			if src != nil {
				_ = src.Close()
			}
			// Context cancellation is NEUTRAL: a shutdown is not a station
			// failure and must not park the (job, station) pair.
			if ctx.Err() != nil {
				return
			}
			e.Planner.RecordOutcome(a.Job.ID, a.Station.Key, ok, nowFn())
		}(a)
	}
	wg.Wait()
	return plan
}

// MonitorOpener maps a station's kind to its monitor dialer.
//
// WARNING: this DIALS LIVE RECEIVERS. Use it only when live receiver
// polling/execution has been explicitly approved by the owner; the fleet
// package's tests never call it with a dialable station.
func MonitorOpener(ctx context.Context, a Assignment) (monitor.Source, error) {
	switch a.Station.Kind {
	case probe.KindKiwi:
		return monitor.KiwiSource(ctx, a.Station.URL)
	case probe.KindOpenWebRX:
		return monitor.OpenWebRXSource(ctx, a.Station.URL, "")
	case probe.KindWebSDR:
		return monitor.WebSDRSource(ctx, a.Station.URL)
	}
	return nil, fmt.Errorf("fleet: unknown station kind %q", a.Station.Kind)
}
