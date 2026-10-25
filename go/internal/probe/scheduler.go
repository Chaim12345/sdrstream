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

	sweepMu sync.Mutex
}

func (s *Scheduler) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Sweep probes every station once and merges outcomes into the catalog.
// A failed directory fetch keeps the catalog's previous records for that
// source: the directory being down must not blank the station list.
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

	keys := make([]string, 0, len(stations))
	for k := range stations {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	workers := s.Workers
	if workers <= 0 {
		workers = 16
	}
	if workers > len(keys) {
		workers = len(keys)
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
	c.UpdatedAt = s.now()
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
