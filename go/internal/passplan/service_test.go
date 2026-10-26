package passplan

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/satellites"
)

func testCfg(t *testing.T, load ElementLoader) Config {
	return Config{
		Lat: 31.77, Lon: 35.21,
		StatePath: filepath.Join(t.TempDir(), "passplan_state.json"),
		Load:      load,
		LoadCatnr: func(int) (string, error) { return "", errors.New("offline") },
		Now:       func() time.Time { return time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC) },
		Sats: []satellites.Sat{
			{Name: "ISS (ZARYA)", NoradID: 25544, Status: "active"},
			{Name: "DEAD", NoradID: 1, Status: "decommissioned"},
		},
		MinElevation: 10,
		Logf:         func(string, ...interface{}) {},
	}
}

func issLoader(group string) (string, error) { return issTLE, nil }

func TestServiceConfiguredTimelineAndRestart(t *testing.T) {
	cfg := testCfg(t, issLoader)
	s := NewService(cfg)
	if err := s.Refresh(); err != nil {
		t.Fatal(err)
	}
	tl := s.P.Timeline()
	if len(tl) == 0 {
		t.Fatal("empty timeline from configured station + cached elements")
	}
	for _, o := range tl {
		if o.Resource != StationResource || o.NoradID != 25544 || o.PeakEl < 10 || o.Status != StatusScheduled {
			t.Fatalf("bad observation %+v", o)
		}
	}
	info := s.InfoJSON().(Info)
	if info.Trackers != 1 || info.Station.Lat != 31.77 || info.Error != "" {
		t.Fatalf("%+v", info)
	}
	// mark one observation running+finished, then "restart": new service, same state file
	id := tl[0].ID
	s.P.Start(id)
	s.P.Finish(id, true, "ok")
	if err := s.P.Save(cfg.StatePath); err != nil {
		t.Fatal(err)
	}
	s2 := NewService(cfg)
	tl2 := s2.P.Timeline()
	if len(tl2) != len(tl) || tl2[0].Status != StatusCompleted {
		t.Fatalf("state not restored: %d vs %d first=%s", len(tl2), len(tl), tl2[0].Status)
	}
	// refreshing after restart does not duplicate or reset the completed one
	if err := s2.Refresh(); err != nil {
		t.Fatal(err)
	}
	tl3 := s2.P.Timeline()
	if len(tl3) != len(tl) || tl3[0].Status != StatusCompleted {
		t.Fatalf("refresh after restart changed plan: %d vs %d", len(tl3), len(tl))
	}
}

func TestServiceNoElementsReportsErrorNotGuess(t *testing.T) {
	s := NewService(testCfg(t, func(string) (string, error) { return "", errors.New("no cache") }))
	if err := s.Refresh(); err == nil {
		t.Fatal("expected error with empty cache")
	}
	if len(s.P.Timeline()) != 0 || s.InfoJSON().(Info).Error == "" {
		t.Fatal("must stay empty and report the reason")
	}
}

func TestServiceRunInjectedTick(t *testing.T) {
	s := NewService(testCfg(t, issLoader))
	ctx, cancel := context.WithCancel(context.Background())
	tick := make(chan time.Time)
	done := make(chan struct{})
	go func() { s.Run(ctx, tick); close(done) }()
	tick <- time.Now() // second refresh proves the loop consumes ticks
	tick <- time.Now()
	cancel()
	<-done
	if len(s.P.Timeline()) == 0 {
		t.Fatal("run produced no timeline")
	}
}
