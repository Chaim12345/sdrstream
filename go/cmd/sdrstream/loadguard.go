package main

import "time"

const (
	// scanHardMaxConnections is the absolute ceiling for
	// --scan-max-connections.
	scanHardMaxConnections = 1000
	// Hard resource caps. Reaching any of them forces an immediate ramp
	// step down, bypassing the load guard's hysteresis.
	guardMaxRSSBytes   = 512 << 20 // 512 MiB
	guardMaxFDs        = 400000
	guardMaxGoroutines = 256000
)

// loadGuard smooths scanner load decisions. Instantaneous load-average reads
// spike under normal scan churn, so the guard works from a rolling mean of
// the load-average/core ratio over a 30s window and applies hysteresis:
//
//   - drop one ramp step only after the rolling mean stays above the drop
//     ratio (85%) for the drop duration (60s)
//   - restore one held step after the rolling mean stays below the restore
//     ratio (65%) for the restore duration (60s)
//
// Hard resource caps (RSS, file descriptors, goroutines) bypass the
// hysteresis and force an immediate step down.
type loadGuard struct {
	window        time.Duration
	dropFor       time.Duration
	restoreFor    time.Duration
	dropRatio     float64
	restoreRatio  float64
	maxRSSBytes   uint64
	maxFDs        int
	maxGoroutines int

	samples    []loadSample
	aboveSince time.Time
	belowSince time.Time
	held       int
}

type loadSample struct {
	at    time.Time
	ratio float64
}

// newLoadGuard builds a guard, falling back to the documented defaults for
// any unset parameter.
func newLoadGuard(window, dropFor, restoreFor time.Duration, dropRatio, restoreRatio float64, maxRSSBytes uint64, maxFDs, maxGoroutines int) *loadGuard {
	if window <= 0 {
		window = 30 * time.Second
	}
	if dropFor <= 0 {
		dropFor = 60 * time.Second
	}
	if restoreFor <= 0 {
		restoreFor = 60 * time.Second
	}
	if dropRatio <= 0 || dropRatio > 1 {
		dropRatio = .85
	}
	if restoreRatio <= 0 || restoreRatio > 1 {
		restoreRatio = .65
	}
	if restoreRatio >= dropRatio {
		restoreRatio = dropRatio * .75
	}
	if maxRSSBytes == 0 {
		maxRSSBytes = guardMaxRSSBytes
	}
	if maxFDs <= 0 {
		maxFDs = guardMaxFDs
	}
	if maxGoroutines <= 0 {
		maxGoroutines = guardMaxGoroutines
	}
	return &loadGuard{window: window, dropFor: dropFor, restoreFor: restoreFor, dropRatio: dropRatio, restoreRatio: restoreRatio, maxRSSBytes: maxRSSBytes, maxFDs: maxFDs, maxGoroutines: maxGoroutines}
}

// observe records one load-average/core ratio sample and returns the rolling
// mean over the configured window.
func (g *loadGuard) observe(now time.Time, ratio float64) float64 {
	g.samples = append(g.samples, loadSample{at: now, ratio: ratio})
	cutoff := now.Add(-g.window)
	keep := g.samples[:0]
	for _, s := range g.samples {
		if !s.at.Before(cutoff) {
			keep = append(keep, s)
		}
	}
	g.samples = keep
	mean := g.mean()
	if mean > g.dropRatio {
		if g.aboveSince.IsZero() {
			g.aboveSince = now
		}
	} else {
		g.aboveSince = time.Time{}
	}
	if mean < g.restoreRatio {
		if g.belowSince.IsZero() {
			g.belowSince = now
		}
	} else {
		g.belowSince = time.Time{}
	}
	return mean
}

func (g *loadGuard) mean() float64 {
	if len(g.samples) == 0 {
		return 0
	}
	sum := 0.0
	for _, s := range g.samples {
		sum += s.ratio
	}
	return sum / float64(len(g.samples))
}

func (g *loadGuard) hardCapHit(rssBytes uint64, fds, goroutines int) bool {
	return rssBytes >= g.maxRSSBytes || (fds > 0 && fds >= g.maxFDs) || goroutines >= g.maxGoroutines
}

// adjust applies the guard to the ramp stage the window gates produced.
// Hard caps drop one step immediately; otherwise a drop needs the rolling
// mean above the drop ratio for the full drop duration, and each further
// drop needs a fresh sustained period. Steps the guard took are held and
// restored one at a time after sustained calm.
func (g *loadGuard) adjust(now time.Time, stage, stageCount int, rssBytes uint64, fds, goroutines int) int {
	if stageCount <= 0 {
		return 0
	}
	stage = min(max(stage, 0), stageCount-1)
	if g.hardCapHit(rssBytes, fds, goroutines) {
		if stage == 0 {
			return 0
		}
		g.held++
		return stage - 1
	}
	if !g.aboveSince.IsZero() && now.Sub(g.aboveSince) >= g.dropFor {
		// One step per sustained period: restart the clock so the next
		// drop needs another full dropFor of overload.
		g.aboveSince = now
		if stage == 0 {
			return 0
		}
		g.held++
		return stage - 1
	}
	if g.held > 0 && !g.belowSince.IsZero() && now.Sub(g.belowSince) >= g.restoreFor {
		g.belowSince = now
		g.held--
		return min(stage+1, stageCount-1)
	}
	return stage
}
