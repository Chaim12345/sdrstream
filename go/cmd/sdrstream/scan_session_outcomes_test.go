package main

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/decode"
	"github.com/chaim12345/sdrstream/go/internal/web"
)

// setupSessionScans registers three queued scan rows the way
// scanKiwiStation does and returns the server, the targets, and the
// key map. The caller resets the global scans slice.
func setupSessionScans(t *testing.T) (*web.Server, []decode.HFTarget, map[string]string) {
	t.Helper()
	scansMu.Lock()
	old := scans
	scans = nil
	scansMu.Unlock()
	t.Cleanup(func() { scansMu.Lock(); scans = old; scansMu.Unlock() })

	server := &web.Server{}
	targets := []decode.HFTarget{
		{Name: "one", Kind: "rtty", DialKHz: 7001, Mode: "usb"},
		{Name: "two", Kind: "rtty", DialKHz: 7002, Mode: "usb"},
		{Name: "three", Kind: "rtty", DialKHz: 7003, Mode: "usb"},
	}
	keys := make(map[string]string, len(targets))
	for _, target := range targets {
		keys[target.Name] = ensureScan(server, web.ScanStatus{StationType: "KiwiSDR", Station: "kiwi.test:8073", FrequencyHz: target.DialKHz * 1000, Mode: target.Mode, State: "queued", UpdatedAt: time.Now()})
	}
	return server, targets, keys
}

func scanStates(t *testing.T) map[string]web.ScanStatus {
	t.Helper()
	scansMu.Lock()
	defer scansMu.Unlock()
	out := make(map[string]web.ScanStatus, len(scans))
	for _, s := range scans {
		out[s.State+"|"+s.Result] = s
	}
	return out
}

func resultFor(target decode.HFTarget) decode.HFResult {
	return decode.HFResult{Target: target, Started: time.Now()}
}

func TestSessionOutcomeCapDefersUnvisited(t *testing.T) {
	server, targets, keys := setupSessionScans(t)
	results := []decode.HFResult{resultFor(targets[0])}
	outcome := markSessionOutcomes(server, keys, targets, results, targets[1:], nil, false)
	if (outcome != scanOutcome{}) {
		t.Fatalf("clean cap outcome = %+v, want zero", outcome)
	}
	states := scanStates(t)
	if _, ok := states["deferred|receiver session target/time cap; next visit resumes after the last processed target"]; !ok {
		t.Fatalf("no cap-deferred row after clean session end: %v", states)
	}
	scansMu.Lock()
	defer scansMu.Unlock()
	deferred := 0
	for _, s := range scans {
		if s.State == "deferred" {
			deferred++
		}
		if s.State == "error" {
			t.Errorf("clean session end marked %v error", s.FrequencyHz)
		}
	}
	if deferred != 2 {
		t.Errorf("deferred rows = %d, want 2 (targets two and three)", deferred)
	}
}

func TestSessionOutcomeFailureMarksRealError(t *testing.T) {
	server, targets, keys := setupSessionScans(t)
	results := []decode.HFResult{resultFor(targets[0])}
	sessionErr := fmt.Errorf("record: %w", errors.New("websocket: close 1006 abnormal closure"))
	outcome := markSessionOutcomes(server, keys, targets, results, targets[1:], sessionErr, false)
	if outcome.refused || outcome.timedOut {
		t.Fatalf("abnormal closure classified as %+v, want plain error", outcome)
	}
	scansMu.Lock()
	defer scansMu.Unlock()
	errRows := 0
	for _, s := range scans {
		if s.State == "deferred" {
			t.Errorf("session error mislabeled as cap-deferred: %+v", s)
		}
		if s.State == "error" {
			errRows++
			if s.Result != sessionErr.Error() {
				t.Errorf("error row result = %q, want the real session error", s.Result)
			}
		}
	}
	if errRows != 2 {
		t.Errorf("error rows = %d, want 2 (targets two and three)", errRows)
	}
}

func TestSessionOutcomeRefusalAndTimeoutStates(t *testing.T) {
	server, targets, keys := setupSessionScans(t)
	outcome := markSessionOutcomes(server, keys, targets, nil, targets, errors.New("connect: too many users"), false)
	if !outcome.refused {
		t.Fatal("too-many-users not classified as refusal")
	}
	states := scanStates(t)
	if _, ok := states["refused/skipped|connect: too many users"]; !ok {
		t.Fatalf("no refused/skipped row: %v", states)
	}
}

func TestSessionOutcomeOwnDeadlineDefersHonestly(t *testing.T) {
	server, targets, keys := setupSessionScans(t)
	results := []decode.HFResult{resultFor(targets[0])}
	ctxErr := fmt.Errorf("record: %w", context.DeadlineExceeded)
	outcome := markSessionOutcomes(server, keys, targets, results, targets[1:], ctxErr, true)
	if !outcome.skipped {
		t.Fatalf("own deadline outcome = %+v, want skipped", outcome)
	}
	scansMu.Lock()
	defer scansMu.Unlock()
	deferred := 0
	for _, s := range scans {
		if s.State == "deferred" {
			deferred++
		}
	}
	if deferred != 2 {
		t.Errorf("own-deadline deferred rows = %d, want 2", deferred)
	}
}
