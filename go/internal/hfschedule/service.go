package hfschedule

// Continuous HF capture service. The Runner already performs one due
// capture per call with all state persisted; the Service turns that into a
// long-running loop: poll for due entries, record at most one capture per
// poll (one receiver connection at a time stays the polite ceiling), and
// reload the broadcast schedule on an interval so a day-old EiBi snapshot
// never drives tonight's listening.

import (
	"context"
	"fmt"
	"time"
)

// Service defaults.
const (
	// DefaultPollInterval matches the cadence cmd/hfschedule used inline.
	DefaultPollInterval = 15 * time.Second
	// DefaultReloadInterval refetches the schedule twice a day; EiBi
	// publishes a seasonal file and single-day changes are rare.
	DefaultReloadInterval = 12 * time.Hour
)

// Service runs the capture schedule continuously.
type Service struct {
	Runner *Runner
	// Load fetches and parses the current schedule. It runs at start and
	// every ReloadInterval; a failed reload keeps the previous schedule
	// because a lost source must not stop a service that still has a
	// usable plan.
	Load func(context.Context) ([]Entry, error)

	PollInterval   time.Duration
	ReloadInterval time.Duration

	// OnAudit is called after every completed capture, recorded or failed:
	// the command wires it to the JSON log, a daemon to its status board.
	OnAudit func(Audit)
	Logf    func(string, ...any)
}

func (s *Service) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
	}
}

// Run blocks until ctx ends or the Runner reports an unrecoverable error
// (corrupt state, lock conflict, filesystem trouble).
func (s *Service) Run(ctx context.Context) error {
	if s.Runner == nil || s.Load == nil {
		return fmt.Errorf("hfschedule service needs a Runner and a Load func")
	}
	poll := s.PollInterval
	if poll <= 0 {
		poll = DefaultPollInterval
	}
	reloadEvery := s.ReloadInterval
	if reloadEvery <= 0 {
		reloadEvery = DefaultReloadInterval
	}
	entries, err := s.Load(ctx)
	if err != nil {
		return fmt.Errorf("load schedule: %w", err)
	}
	s.logf("hfschedule: service up, %d schedule entries, poll %s, reload %s", len(entries), poll, reloadEvery)
	nextReload := time.Now().Add(reloadEvery)
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		a, err := s.Runner.RunOnce(ctx, entries)
		if err != nil {
			return err
		}
		if a != nil {
			s.logf("hfschedule: %s on %s via %s: %s", a.Occurrence.Entry.Target.Name, a.Occurrence.Entry.Target.CarrierKHz, a.Receiver.URL, a.Status)
			if s.OnAudit != nil {
				s.OnAudit(*a)
			}
		}
		if time.Now().After(nextReload) {
			if fresh, err := s.Load(ctx); err != nil {
				s.logf("hfschedule: schedule reload failed, keeping %d entries: %v", len(entries), err)
			} else if len(fresh) == 0 {
				s.logf("hfschedule: schedule reload returned no entries, keeping %d", len(entries))
			} else {
				entries = fresh
				s.logf("hfschedule: schedule reloaded, %d entries", len(entries))
			}
			nextReload = time.Now().Add(reloadEvery)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
