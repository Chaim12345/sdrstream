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
}

// RunOnce plans jobs against the snapshot and executes each assignment:
// open the station's source, run the work, close the source (always),
// record the outcome. Sources are closed even when open succeeded but
// work failed; a half-run assignment never leaks a receiver slot.
func (e *Executor) RunOnce(ctx context.Context, snap probe.Snapshot, jobs []Job, now time.Time) Plan {
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
			ok := false
			src, err := e.Open(ctx, a)
			if err == nil {
				err = e.Work(ctx, a, src)
				_ = src.Close()
			}
			ok = err == nil
			e.Planner.RecordOutcome(a.Job.ID, a.Station.Key, ok, now)
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
