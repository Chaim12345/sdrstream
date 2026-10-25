package probe

import (
	"context"
	"sort"
	"sync"
	"time"
)

// Source lists the stations to sweep. Both directory fetches are
// injectable so the scheduler's tests never touch the network.
type Source struct {
	// OpenDirectory lists receiverbook stations (OpenWebRX + WebSDR).
	OpenDirectory func(ctx context.Context) ([]Record, error)
	// KiwiDirectory lists KiwiSDR receivers.
	KiwiDirectory func(ctx context.Context) ([]Record, error)
	// Manual stations always swept, kind-prefixed like the scan roster.
	Manual []Record
}

// Scheduler runs periodic sweeps of every listed station into a Catalog.
// Safe for concurrent use; Sweep is serialized (one sweep at a time).
type Scheduler struct {
	Source   Source
	Prober   Prober
	Workers  int           // concurrent probes per sweep; default 16
	Interval time.Duration // time between sweeps; default 1h
	Now      func() time.Time
	// Path, when set, persists the catalog after every completed sweep so
	// a restart starts from the last known station capabilities.
	Path string

	sweepMu sync.Mutex
}

func (s *Scheduler) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Sweep probes every DUE station once and merges outcomes into the
// catalog. A station whose NextCheckAt is in the future is skipped
// untouched: consecutive failures push NextCheckAt out with a doubling
// backoff (see Backoff), so a dead receiver costs one probe per backoff
// window, not one per sweep. A failed directory fetch keeps the catalog's
// previous records for that source: the directory being down must not
// blank the station list.
func (s *Scheduler) Sweep(ctx context.Context, c *Catalog) error {
	s.sweepMu.Lock()
	defer s.sweepMu.Unlock()

	stations := map[string]Record{}
	for _, rec := range s.Source.Manual {
		stations[rec.Key] = rec
	}
	var firstErr error
	if s.Source.OpenDirectory != nil {
		if recs, err := s.Source.OpenDirectory(ctx); err != nil {
			firstErr = err
		} else {
			for _, rec := range recs {
				stations[rec.Key] = rec
			}
		}
	}
	if s.Source.KiwiDirectory != nil {
		if recs, err := s.Source.KiwiDirectory(ctx); err != nil && firstErr == nil {
			firstErr = err
		} else if err == nil {
			for _, rec := range recs {
				stations[rec.Key] = rec
			}
		}
	}
	if len(stations) == 0 && firstErr != nil {
		return firstErr
	}

	// Due filtering: skip stations whose backoff window has not elapsed.
	// New stations (zero NextCheckAt) are always due.
	sweepStart := s.now()
	keys := make([]string, 0, len(stations))
	for k := range stations {
		if prev, ok := c.Get(k); ok && !prev.NextCheckAt.IsZero() && sweepStart.Before(prev.NextCheckAt) {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return firstErr
	}

	workers := s.Workers
	if workers <= 0 {
		workers = 16
	}
	if workers > len(keys) {
		workers = len(keys)
	}
	base := s.Interval
	if base <= 0 {
		base = time.Hour
	}
	jobs := make(chan string)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for key := range jobs {
				rec := stations[key]
				if s.Prober != nil {
					s.Prober.Probe(ctx, &rec)
				}
				checked := s.now()
				if rec.Reachable {
					rec.NextCheckAt = checked.Add(base)
				} else {
					prev, _ := c.Get(key)
					rec.NextCheckAt = checked.Add(Backoff(base, prev.ConsecFailures+1))
				}
				mu.Lock()
				c.Update(rec)
				mu.Unlock()
			}
		}()
	}
	for _, key := range keys {
		select {
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return ctx.Err()
		case jobs <- key:
		}
	}
	close(jobs)
	wg.Wait()
	c.Touch(s.now())
	if s.Path != "" {
		if err := c.Save(s.Path); err != nil {
			return err
		}
	}
	return firstErr
}

// Run sweeps on Interval until ctx is done. The first sweep starts
// immediately so a boot does not sit on an empty catalog for an hour.
func (s *Scheduler) Run(ctx context.Context, c *Catalog) {
	interval := s.Interval
	if interval <= 0 {
		interval = time.Hour
	}
	for {
		_ = s.Sweep(ctx, c)
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}
