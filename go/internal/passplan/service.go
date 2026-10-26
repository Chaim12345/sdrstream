package passplan

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/satellites"
	"github.com/chaim12345/sdrstream/go/internal/tle"
	"github.com/chaim12345/sdrstream/go/internal/tracking"
)

// StationResource is the single resource every observation needs: one
// antenna/receiver at the configured station can follow one satellite at a
// time, so overlapping passes conflict.
const StationResource = "station"

// ElementLoader returns a CelesTrak group's element text from the existing
// cache. It must not fetch. The production loader is CachedElements.
type ElementLoader func(group string) (string, error)

// CatnrLoader is the same for one object by catalogue number.
type CatnrLoader func(norad int) (string, error)

// CachedElements reads only the on-disk TLE cache (offline=true, no network).
func CachedElements(group string) (string, error) { return tle.Load(group, 24, true, 0) }

// CachedCatnr reads only the cached single-object element set.
func CachedCatnr(norad int) (string, error) { return tle.LoadCatnr(norad, 24, true, 0) }

// Config configures a Service. Lat/Lon are the configured station location
// (cmd passes the existing -feeds-lat/-feeds-lon values).
type Config struct {
	Lat, Lon     float64
	StatePath    string // e.g. web.StateFile("passplan_state.json"); empty = no persistence
	Load         ElementLoader
	LoadCatnr    CatnrLoader
	Now          func() time.Time
	Strategy     string
	Priority     []string // satellite names, first = highest (lowest Priority number)
	MinElevation float64
	Sats         []satellites.Sat // default satellites.All()
	Logf         func(string, ...interface{})
}

// Service owns a Planner, rebuilds it from cached elements and persists it.
// It plans only: it never starts a capture or contacts a receiver.
type Service struct {
	cfg       Config
	P         *Planner
	refreshMu sync.Mutex // serialises Refresh; mu guards info
	mu        sync.Mutex
	info      Info
}

// Info is the last refresh outcome, served alongside the timeline.
type Info struct {
	LastRefresh time.Time `json:"last_refresh"`
	Trackers    int       `json:"satellites_with_elements"`
	Error       string    `json:"error,omitempty"`
	Strategy    string    `json:"strategy"`
	Station     struct {
		Lat float64 `json:"lat"`
		Lon float64 `json:"lon"`
	} `json:"station"`
}

// NewService builds a Service and restores saved state. A corrupt state file
// is logged and ignored (the plan is rebuilt on the first Refresh).
func NewService(cfg Config) *Service {
	if cfg.Load == nil {
		cfg.Load = CachedElements
	}
	if cfg.LoadCatnr == nil {
		cfg.LoadCatnr = CachedCatnr
	}
	if cfg.Logf == nil {
		cfg.Logf = log.Printf
	}
	if cfg.Sats == nil {
		cfg.Sats = satellites.All()
	}
	s := &Service{cfg: cfg, P: New(nil, cfg.Strategy)}
	if cfg.Now != nil {
		s.P.Now = cfg.Now
	}
	if cfg.StatePath != "" {
		if err := s.P.Load(cfg.StatePath); err != nil {
			cfg.Logf("passplan: ignoring unreadable state: %v", err)
		}
	}
	s.info.Strategy = s.P.Strategy
	s.info.Station.Lat, s.info.Station.Lon = cfg.Lat, cfg.Lon
	return s
}

func (s *Service) priorityOf(name string, idx int) int {
	for i, n := range s.cfg.Priority {
		if n == name {
			return i
		}
	}
	return len(s.cfg.Priority) + idx
}

// Refresh recomputes the plan from the element cache and saves it.
func (s *Service) Refresh() error {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	trackers := map[int]*tracking.Tracker{}
	var mons []Monitored
	groups := map[string]string{}
	for idx, sat := range s.cfg.Sats {
		if sat.Status != "active" || sat.NoradID == 0 {
			continue
		}
		group := sat.Group
		if group == "" {
			group = "amateur"
		}
		text, ok := groups[group]
		if !ok {
			t, err := s.cfg.Load(group)
			if err != nil {
				t = ""
			}
			groups[group], text = t, t
		}
		var tr *tracking.Tracker
		var err error
		if text != "" {
			tr, err = tracking.FromTLEText(text, sat.Name)
		} else {
			err = fmt.Errorf("no cached elements for group %s", group)
		}
		if err != nil {
			if single, cerr := s.cfg.LoadCatnr(sat.NoradID); cerr == nil && single != "" {
				tr, err = tracking.FromTLEText(single, sat.Name)
			}
		}
		if err != nil || tr == nil {
			continue
		}
		trackers[sat.NoradID] = tr
		mons = append(mons, Monitored{
			ID: fmt.Sprintf("sat-%d", sat.NoradID), Name: sat.Name, NoradID: sat.NoradID,
			Resource: StationResource, MinElevation: s.cfg.MinElevation,
			TaskStartElevation: 10, Priority: s.priorityOf(sat.Name, idx), Enabled: true,
		})
	}
	s.mu.Lock()
	s.info.LastRefresh = s.P.Now().UTC()
	s.info.Trackers = len(trackers)
	s.info.Error = ""
	s.mu.Unlock()
	if len(trackers) == 0 {
		err := fmt.Errorf("no cached orbital elements; run the pass table once online to fill the TLE cache")
		s.mu.Lock()
		s.info.Error = err.Error()
		s.mu.Unlock()
		return err
	}
	s.P.SetFinder(TrackerFinder{Trackers: trackers, Lat: s.cfg.Lat, Lon: s.cfg.Lon})
	_, gerr := s.P.Generate(mons)
	if gerr != nil {
		s.mu.Lock()
		s.info.Error = gerr.Error()
		s.mu.Unlock()
	}
	if s.cfg.StatePath != "" {
		if err := s.P.Save(s.cfg.StatePath); err != nil {
			s.cfg.Logf("passplan: save state: %v", err)
		}
	}
	return gerr
}

// Run refreshes immediately and on every tick until ctx ends. The tick
// channel is injected so tests need no real timers (cmd passes a
// time.Ticker's channel).
func (s *Service) Run(ctx context.Context, tick <-chan time.Time) {
	if err := s.Refresh(); err != nil {
		s.cfg.Logf("passplan: %v", err)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick:
			if err := s.Refresh(); err != nil {
				s.cfg.Logf("passplan: %v", err)
			}
		}
	}
}

// TimelineJSON serves the web contract: the observation list.
func (s *Service) TimelineJSON() interface{} { return s.P.Timeline() }

// InfoJSON reports the last refresh outcome.
func (s *Service) InfoJSON() interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.info
}
