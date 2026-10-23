package satsched

import (
	"testing"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/satellites"
	"github.com/chaim12345/sdrstream/go/internal/tracking"
)

func satByNameForTest(t *testing.T, name string) satellites.Sat {
	t.Helper()
	for _, sat := range satellites.Table {
		if sat.Name == name {
			return sat
		}
	}
	t.Fatalf("catalogue satellite %q not found", name)
	return satellites.Sat{}
}

// Regression: during a dated transmission event the event downlink must be
// the chase target. The ISS catalogue entry lists 145.825 MHz first in
// Beacons, and the chase prefers the first beacon; without the override
// dropping the beacon list, an ARISS SSTV series on 437.550 MHz was chased
// on the legacy 145.825 MHz beacon instead.
func TestEventOverrideDefeatsBeaconPreference(t *testing.T) {
	iss := satByNameForTest(t, "ISS")
	if len(iss.Beacons) == 0 {
		t.Fatal("test requires ISS to carry catalogue beacons")
	}
	// Inside the ARISS SSTV Series 34 window (2026-10-22 15:30 UTC to
	// 2026-10-26 17:45 UTC).
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	got, eventName := applyEventOverride(iss, now, now.Add(24*time.Hour))
	if eventName == "" {
		t.Fatal("no event applied inside the Series 34 window")
	}
	if got.DownlinkHz != 437550000 {
		t.Fatalf("event downlink = %d, want 437550000", got.DownlinkHz)
	}
	if got.Mode != "FM" {
		t.Fatalf("event mode = %q, want FM", got.Mode)
	}
	if len(got.Beacons) != 0 {
		t.Fatalf("beacon list survived the event override: %v", got.Beacons)
	}
}

// The same satellite outside the window keeps its catalogue plan untouched:
// no event, no mutation, beacon preference intact.
func TestEventOverrideLeavesSatelliteAloneOutsideWindow(t *testing.T) {
	iss := satByNameForTest(t, "ISS")
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	got, eventName := applyEventOverride(iss, now, now.Add(24*time.Hour))
	if eventName != "" {
		t.Fatalf("event %q applied outside its window", eventName)
	}
	if got.DownlinkHz != iss.DownlinkHz || len(got.Beacons) != len(iss.Beacons) {
		t.Fatalf("catalogue entry mutated outside an event window: %+v", got)
	}
}

// Past-pass handling: a pass whose recording window has fully closed must be
// treated as elapsed (plan() skips it), while a pass in progress - risen but
// not yet set - stays plannable as an immediate chase.
func TestRecordingWindowElapsedSkipsSetKeepsInProgress(t *testing.T) {
	now := time.Date(2026, 10, 23, 9, 6, 0, 0, time.UTC)
	set := tracking.Pass{
		Rise: now.Add(-3 * time.Hour),
		Set:  now.Add(-2*time.Hour + 50*time.Minute),
	}
	start, end := recordingWindow(set, 90, 60)
	if !elapsed(end, now) {
		t.Fatalf("fully-set pass not elapsed: window %s..%s at %s", start, end, now)
	}
	inProgress := tracking.Pass{
		Rise: now.Add(-4 * time.Minute),
		Set:  now.Add(6 * time.Minute),
	}
	_, end = recordingWindow(inProgress, 90, 60)
	if elapsed(end, now) {
		t.Fatalf("in-progress pass treated as elapsed: window ends %s at %s", end, now)
	}
	future := tracking.Pass{
		Rise: now.Add(17 * time.Hour),
		Set:  now.Add(17*time.Hour + 10*time.Minute),
	}
	_, end = recordingWindow(future, 90, 60)
	if elapsed(end, now) {
		t.Fatalf("future pass treated as elapsed: window ends %s at %s", end, now)
	}
}
