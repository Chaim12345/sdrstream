package satellites

import (
	"testing"
	"time"
)

func at(t *testing.T, ts string) time.Time {
	t.Helper()
	tm, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		t.Fatal(err)
	}
	return tm
}

func TestActiveEventSeries34Window(t *testing.T) {
	// The dated table holds ARISS SSTV Series 34 on 437.550 MHz,
	// 2026-10-22T15:10Z through 2026-10-26T17:45Z.
	ev := ActiveEvent("ISS", at(t, "2026-10-23T12:00:00Z"))
	if ev == nil {
		t.Fatal("ISS event must be active during Series 34")
	}
	if ev.DownlinkHz != 437550000 || ev.ImageMode != "Robot36" {
		t.Fatalf("override = %.4f MHz %s, want 437.5500 Robot36",
			float64(ev.DownlinkHz)/1e6, ev.ImageMode)
	}
	if ActiveEvent("ISS", at(t, "2026-10-27T00:00:00Z")) != nil {
		t.Fatal("event must expire after 2026-10-26T17:45Z")
	}
	if ActiveEvent("ISS", at(t, "2026-10-22T15:00:00Z")) != nil {
		t.Fatal("event must not apply before its start")
	}
	if ActiveEvent("NOAA 15", at(t, "2026-10-23T12:00:00Z")) != nil {
		t.Fatal("no event may exist for a decommissioned bird")
	}
	if ActiveEvent("iss", at(t, "2026-10-23T12:00:00Z")) == nil {
		t.Fatal("lookup must be case-insensitive")
	}
}

func TestEventCoveringLookahead(t *testing.T) {
	// Planning 48h ahead on Oct 24 must already see the event ending
	// Oct 26: frequency and receiver choice follow the event.
	if EventCovering("ISS", at(t, "2026-10-24T00:00:00Z"), at(t, "2026-10-26T00:00:00Z")) == nil {
		t.Fatal("lookahead intersecting the event must select it")
	}
	if EventCovering("ISS", at(t, "2026-10-27T00:00:00Z"), at(t, "2026-10-28T00:00:00Z")) != nil {
		t.Fatal("window after the event must not select it")
	}
	if EventCovering("ISS", at(t, "2026-10-20T00:00:00Z"), at(t, "2026-10-21T00:00:00Z")) != nil {
		t.Fatal("window before the event must not select it")
	}
}
