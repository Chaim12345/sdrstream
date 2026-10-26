package passplan

import (
	"context"
	"fmt"
	"time"
)

// RunFunc executes one planned observation. It is supplied by the caller
// (for example a satsched adapter); passplan itself never records, tunes or
// contacts a receiver.
type RunFunc func(ctx context.Context, o Observation) error

// RunDue runs at most one observation whose task window has begun (or begins
// within lead) and has not ended. It moves the observation scheduled ->
// running -> completed/failed around fn, so the timeline shows real state.
// It returns the observation it ran, or nil when nothing was due.
//
// A panic or an fn error marks the observation failed; ctx cancellation
// during fn is also recorded as failed with the context error. A running
// observation is never started twice: Start fails unless it is scheduled.
func (p *Planner) RunDue(ctx context.Context, lead time.Duration, fn RunFunc) (*Observation, error) {
	next := p.Next()
	if next == nil {
		return nil, nil
	}
	now := p.Now()
	if next.TaskStart.After(now.Add(lead)) {
		return nil, nil
	}
	if err := p.Start(next.ID); err != nil {
		return nil, err
	}
	var runErr error
	func() {
		defer func() {
			if r := recover(); r != nil {
				runErr = fmt.Errorf("panic: %v", r)
			}
		}()
		runErr = fn(ctx, *next)
	}()
	if runErr != nil {
		_ = p.Finish(next.ID, false, runErr.Error())
		return next, runErr
	}
	_ = p.Finish(next.ID, true, "")
	return next, nil
}
