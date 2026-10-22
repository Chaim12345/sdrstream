package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/chaim12345/sdrstream/go/internal/decode"
)

func resetRotationCursors() {
	openProfileMu.Lock()
	openProfilesByHost = map[string][]string{}
	openProfileNext = map[string]int{}
	openProfileMu.Unlock()
	kiwiCursorMu.Lock()
	kiwiCursorByHost = map[string]string{}
	kiwiCursorMu.Unlock()
}

func TestScanRosterMergesManualAndDynamicWithoutDuplicates(t *testing.T) {
	r := newScanRoster(nil)
	r.AddManual("KiwiSDR|http://manual.test:8073/")
	r.SetDynamic([]string{
		"KiwiSDR|http://manual.test:8073", // same host:port as the manual entry
		"OpenWebRX|http://dynamic.test:8073/",
	})
	got := r.Snapshot()
	if len(got) != 2 {
		t.Fatalf("snapshot = %v, want 2 deduped stations", got)
	}
	if got[0] != "KiwiSDR|http://manual.test:8073/" {
		t.Errorf("first station = %q, want the manual entry to win", got[0])
	}
	if got[1] != "OpenWebRX|http://dynamic.test:8073/" {
		t.Errorf("second station = %q, want the dynamic entry", got[1])
	}
}

func TestScanRosterRefreshKeepsPreviousListOnFailure(t *testing.T) {
	r := newScanRoster(nil)
	r.SetDynamic([]string{"KiwiSDR|http://live.test:8073"})
	r.refresh = func(context.Context) ([]string, error) { return nil, errors.New("directory down") }
	if err := r.Refresh(context.Background()); err == nil {
		t.Fatal("refresh error was swallowed")
	}
	got := r.Snapshot()
	if len(got) != 1 || got[0] != "KiwiSDR|http://live.test:8073" {
		t.Fatalf("snapshot after failed refresh = %v, want the previous station", got)
	}
}

func TestScanRosterRefreshReplacesDynamicHalfOnly(t *testing.T) {
	r := newScanRoster(nil)
	r.AddManual("WebSDR|http://manual.test:8901")
	r.SetDynamic([]string{"KiwiSDR|http://old.test:8073"})
	r.refresh = func(context.Context) ([]string, error) {
		return []string{"OpenWebRX|http://new.test:8073"}, nil
	}
	if err := r.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := r.Snapshot()
	if len(got) != 2 {
		t.Fatalf("snapshot = %v, want manual + refreshed dynamic", got)
	}
	if got[0] != "WebSDR|http://manual.test:8901" || got[1] != "OpenWebRX|http://new.test:8073" {
		t.Fatalf("snapshot = %v, want manual then the new dynamic station", got)
	}
}

func TestRotateHFTargetsResumesAfterLastProcessed(t *testing.T) {
	targets := []decode.HFTarget{{Name: "a"}, {Name: "b"}, {Name: "c"}}
	got := rotateHFTargets(targets, "b")
	if len(got) != 3 || got[0].Name != "c" || got[1].Name != "a" || got[2].Name != "b" {
		t.Fatalf("rotation = %+v, want c,a,b", got)
	}
	if all := rotateHFTargets(targets, ""); all[0].Name != "a" {
		t.Fatalf("empty cursor should start at the first target: %+v", all)
	}
}

func TestKiwiCursorAdvancesAfterProgress(t *testing.T) {
	resetRotationCursors()
	host := "kiwi.test:8073"
	targets := []decode.HFTarget{{Name: "a"}, {Name: "b"}, {Name: "c"}}
	if got := nextKiwiTargets(host, targets); got[0].Name != "a" {
		t.Fatalf("first visit starts at %q, want a", got[0].Name)
	}
	noteKiwiProgress(host, []decode.HFResult{{Target: targets[1]}})
	if got := nextKiwiTargets(host, targets); got[0].Name != "c" {
		t.Fatalf("second visit starts at %q, want c", got[0].Name)
	}
	noteKiwiProgress(host, []decode.HFResult{{Target: targets[2]}})
	if got := nextKiwiTargets(host, targets); got[0].Name != "a" {
		t.Fatalf("rotation did not wrap: %q", got[0].Name)
	}
}

func TestOpenWebRXProfileRotation(t *testing.T) {
	resetRotationCursors()
	host := "owrx.test:8073"
	if got := nextOpenWebRXProfile(host); got != "" {
		t.Fatalf("first visit profile = %q, want the server default", got)
	}
	noteOpenWebRXProfiles(host, "", []string{"2m", "70cm", "HF"})
	if got := nextOpenWebRXProfile(host); got != "2m" {
		t.Fatalf("second visit profile = %q, want 2m", got)
	}
	if got := nextOpenWebRXProfile(host); got != "70cm" {
		t.Fatalf("third visit profile = %q, want 70cm", got)
	}
	// A server-reported active profile resets the cursor after it, so a
	// failed/renamed profile does not skip the rest of the list forever.
	noteOpenWebRXProfiles(host, "HF", []string{"2m", "70cm", "HF"})
	if got := nextOpenWebRXProfile(host); got != "2m" {
		t.Fatalf("cursor after active HF = %q, want wrap to 2m", got)
	}
}

func TestAddManualRejectsBareHostLoudly(t *testing.T) {
	var logs []string
	r := newScanRoster(func(format string, args ...interface{}) {
		logs = append(logs, fmt.Sprintf(format, args...))
	})
	// The QA finding: a bare host:port (no Kind| prefix) was stored by
	// AddManual and then silently dropped by Snapshot's "|" split, so the
	// station never scanned and nothing said why.
	r.AddManual("manual.test:8073", "KiwiSDR|good.test:8073")
	got := r.Snapshot()
	if len(got) != 1 || got[0] != "KiwiSDR|good.test:8073" {
		t.Fatalf("snapshot = %v, want only the prefixed station", got)
	}
	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "manual.test:8073") || !strings.Contains(joined, "Kind|") {
		t.Fatalf("drop must be logged with the entry and the fix, got: %q", joined)
	}
}

func TestAddManualRejectsEmptyHostLoudly(t *testing.T) {
	var logs []string
	r := newScanRoster(func(format string, args ...interface{}) {
		logs = append(logs, fmt.Sprintf(format, args...))
	})
	r.AddManual("KiwiSDR|", "|host-with-guessed-kind.test:8073")
	got := r.Snapshot()
	// "KiwiSDR|" (no host) is dropped; "|host" keeps the existing
	// guess-the-kind fallback, but both must log.
	if len(got) != 1 || got[0] != "|host-with-guessed-kind.test:8073" {
		t.Fatalf("snapshot = %v, want only the empty-kind entry kept", got)
	}
	if len(logs) != 2 {
		t.Fatalf("want 2 log lines (empty host drop, empty kind guess), got %v", logs)
	}
}
