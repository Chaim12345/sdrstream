package tracking

import (
	"math"
	"testing"
	"time"
)

// The underlying sgp4 library reads a time's civil fields as if they were
// UTC and ignores the zone offset (verified against python-sgp4 on
// 2026-10-23). tracking must normalise, so a local-zone time.Now() can never
// shift a propagation.
func TestLookAngleIgnoresTimeZoneTag(t *testing.T) {
	tracker, err := FromTLEText(tleISS, "ISS")
	if err != nil {
		t.Fatal(err)
	}
	instant := time.Date(2026, 10, 23, 7, 11, 45, 0, time.UTC)
	zones := []*time.Location{
		time.UTC,
		time.FixedZone("IDT", 3*3600),
		time.FixedZone("PST", -8*3600),
	}
	var reference float64
	for i, zone := range zones {
		look, err := tracker.LookAngle(instant.In(zone), 51.80, 5.71, 0)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			reference = look.Elevation
			continue
		}
		if math.Abs(look.Elevation-reference) > 1e-9 {
			t.Fatalf("zone %s shifted the propagation: el %.4f vs %.4f",
				zone, look.Elevation, reference)
		}
	}
}

func TestFindPassesIgnoresTimeZoneTag(t *testing.T) {
	tracker, err := FromTLEText(tleISS, "ISS")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 22, 23, 10, 0, 0, time.UTC)
	reference, err := tracker.FindPasses(51.80, 5.71, start, 24, 25)
	if err != nil {
		t.Fatal(err)
	}
	shifted, err := tracker.FindPasses(51.80, 5.71, start.In(time.FixedZone("IDT", 3*3600)), 24, 25)
	if err != nil {
		t.Fatal(err)
	}
	if len(reference) != len(shifted) {
		t.Fatalf("zone tag changed the pass count: %d vs %d", len(reference), len(shifted))
	}
	for i := range reference {
		if !reference[i].Rise.Equal(shifted[i].Rise) {
			t.Fatalf("pass %d rise moved with the zone tag: %s vs %s",
				i, reference[i].Rise, shifted[i].Rise)
		}
		if reference[i].Rise.Location() != time.UTC && reference[i].Rise.Location().String() != "UTC" {
			t.Fatalf("pass %d rise not returned in UTC: %v", i, reference[i].Rise.Location())
		}
	}
}
