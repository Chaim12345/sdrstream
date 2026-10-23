// Package satsched is the automatic multi-satellite pass scheduler: it plans
// the next day of passes across the satellites that have a receiver, picks
// what to record by priority, waits for acquisition, chases the pass with
// Doppler correction, then decodes what it heard (NOAA APT to images) and
// files everything with the web UI.
//
// The idea mirrors the "Tracking" module of SDR-Radio.com (closed source -
// ideas only): a priority-ordered satellite list, pre-tuning before AOS and
// automatic recording. All implementation here is original.
package satsched

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/catalogue"
	"github.com/chaim12345/sdrstream/go/internal/decode"
	"github.com/chaim12345/sdrstream/go/internal/monitor"
	"github.com/chaim12345/sdrstream/go/internal/satellite"
	"github.com/chaim12345/sdrstream/go/internal/satellites"
	"github.com/chaim12345/sdrstream/go/internal/tracking"
	"github.com/chaim12345/sdrstream/go/internal/web"
)

// Options configures a Service.
type Options struct {
	CapturesDir  string
	MinElevation float64  // default 25: low passes are noise on a shared receiver
	LookaheadH   float64  // default 24
	LeadSeconds  float64  // pre-tune before AOS; default 90
	TailSeconds  float64  // keep recording after LOS; default 60
	Priority     []string // satellite names, highest first; default: ISS (see below)
	Offline      bool     // use cached TLEs only
	Server       *web.Server
	Say          func(string) // progress lines; nil logs
}

// Status is one scheduler decision, kept for the API.
type Status struct {
	Satellite string    `json:"satellite"`
	Receiver  string    `json:"receiver"`
	Rise      time.Time `json:"rise"`
	Peak      time.Time `json:"peak"`
	MaxEl     float64   `json:"max_elevation"`
	State     string    `json:"state"` // planned, chasing, decoded, failed, skipped
	Detail    string    `json:"detail,omitempty"`
}

// Service runs the scheduling loop.
type Service struct {
	opts   Options
	done   map[string]bool // "satellite@rise" already attempted
	status []Status
	say    func(string)
}

// New builds a Service.
func New(opts Options) *Service {
	if opts.MinElevation <= 0 {
		opts.MinElevation = 25
	}
	if opts.LookaheadH <= 0 {
		opts.LookaheadH = 24
	}
	if opts.LeadSeconds <= 0 {
		opts.LeadSeconds = 90
	}
	if opts.TailSeconds <= 0 {
		opts.TailSeconds = 60
	}
	if len(opts.Priority) == 0 {
		// NOAA 15/18/19 were decommissioned in 2025 (NOAA OSPO) and their
		// APT transmitters are unvalidated - 2026-10-23 attempts were
		// noise-only. Auto-scheduling must not default to retired birds;
		// they stay selectable by explicit request. ISS leads: current,
		// validated traffic (SSTV events, APRS).
		opts.Priority = []string{"ISS"}
	}
	say := opts.Say
	if say == nil {
		say = func(line string) { log.Printf("satsched: %s", line) }
	}
	return &Service{opts: opts, done: map[string]bool{}, say: say}
}

// PlanOnce prints the scheduler's current pick and returns. It is the
// -once dry run: same planning path as Run, no waiting and no recording.
func (s *Service) PlanOnce() error {
	next, wait := s.plan()
	if next == nil {
		s.say(fmt.Sprintf("nothing recordable in the next %.0f h (replan in %s)", s.opts.LookaheadH, wait))
		return nil
	}
	// Anchor the plan: a clock time alone reads as a past pass when the
	// next usable pass is after midnight, so state when the plan was made
	// and that every printed time carries its date.
	fmt.Printf("plan anchored at %s UTC; all times below UTC with dates\n",
		time.Now().UTC().Format("2006-01-02 15:04:05"))
	fmt.Println(next.plan.Summary())
	return nil
}

// Statuses returns a copy of the decision log, newest last.
func (s *Service) Statuses() []Status {
	out := make([]Status, len(s.status))
	copy(out, s.status)
	return out
}

// StatusesJSON is Statuses as interface{}, so the web package can serve the
// log without importing this package (which imports the web package).
func (s *Service) StatusesJSON() interface{} { return s.Statuses() }

// Run schedules and records passes until ctx is cancelled.
func (s *Service) Run(ctx context.Context) error {
	for {
		next, wait := s.plan()
		if next == nil {
			s.say(fmt.Sprintf("no recordable passes in the next %.0f h; replanning in %s",
				s.opts.LookaheadH, wait))
			if !sleepCtx(ctx, wait) {
				return ctx.Err()
			}
			continue
		}
		if next.event != "" {
			s.say(fmt.Sprintf("event override: %s - tuning %.4f MHz per dated table",
				next.event, float64(next.plan.Satellite.DownlinkHz)/1e6))
		}
		s.say(fmt.Sprintf("next: %s over %s, rise %s UTC (peak %.0f deg), waiting %s",
			next.plan.Satellite.Name, shortURL(next.plan.Receiver.URL),
			next.plan.Passes[0].Rise.Format("15:04:05"), next.plan.Passes[0].MaxElevation(),
			time.Until(next.plan.Start).Round(time.Second)))
		if !sleepCtx(ctx, time.Until(next.plan.Start)) {
			return ctx.Err()
		}
		s.chaseAndDecode(ctx, next)
	}
}

type candidate struct {
	sat      satellites.Sat
	plan     satellite.ChasePlan
	priority int
	key      string
	event    string // dated event that overrode the catalogue downlink, "" if none
}

// applyEventOverride retargets a catalogue entry to a dated transmission
// event (e.g. an ARISS SSTV series on 70cm) covering [now, windowEnd]: the
// event frequency and mode become the chase target and the catalogue beacon
// list is dropped, because the beacon preference belongs to the normal
// operating plan and must not win the frequency back during an event.
// Returns the event name, or "" and the unchanged entry when no event
// applies.
func applyEventOverride(sat satellites.Sat, now, windowEnd time.Time) (satellites.Sat, string) {
	ev := satellites.EventCovering(sat.Name, now, windowEnd)
	if ev == nil {
		return sat, ""
	}
	sat.DownlinkHz = ev.DownlinkHz
	sat.Mode = ev.Mode
	sat.Beacons = nil
	return sat, ev.Name
}

// recordingWindow pads a pass into the recording interval.
func recordingWindow(pass tracking.Pass, leadSeconds, tailSeconds float64) (time.Time, time.Time) {
	return pass.Rise.Add(-time.Duration(leadSeconds * float64(time.Second))),
		pass.Set.Add(time.Duration(tailSeconds * float64(time.Second)))
}

// elapsed reports whether the recording window closed before now.
func elapsed(end, now time.Time) bool { return end.Before(now) }

// plan finds the best next pass: earliest rise wins; when two passes overlap,
// the satellite earlier in the priority list wins.
func (s *Service) plan() (*candidate, time.Duration) {
	var candidates []candidate
	for priority, name := range s.opts.Priority {
		sat, err := satByName(name)
		if err != nil {
			continue
		}
		if sat.Status != "active" {
			// Intentional refusal, not an oversight: retired birds (NOAA
			// 15/18/19, decommissioned per NOAA OSPO 2025; APT
			// unvalidated) are never scheduled, even on explicit
			// request. See README "Satellite scheduling".
			s.say(fmt.Sprintf("%s skipped: status %q - retired satellites are not scheduled (intentional; see README)", name, sat.Status))
			continue
		}
		// A dated transmission event (e.g. an ARISS SSTV series on 70cm)
		// overrides the catalogue downlink for the whole lookahead window:
		// tune the event frequency and vet receiver coverage against it,
		// never silently against the legacy default.
		now := time.Now()
		windowEnd := now.Add(time.Duration(s.opts.LookaheadH * float64(time.Hour)))
		sat, eventName := applyEventOverride(sat, now, windowEnd)
		receivers := catalogue.ForFrequency(float64(sat.DownlinkHz))
		if len(receivers) == 0 {
			continue
		}
		receiver := receivers[0] // verified receivers sort first
		plan, err := satellite.PlanPass(sat, &receiver, nil, nil, time.Now(),
			s.opts.LookaheadH, s.opts.MinElevation, s.opts.LeadSeconds,
			s.opts.TailSeconds, "", s.opts.Offline)
		if err != nil || len(plan.Passes) == 0 {
			continue
		}
		for _, pass := range plan.Passes {
			key := fmt.Sprintf("%s@%s", sat.Name, pass.Rise.UTC().Format(time.RFC3339))
			if s.done[key] {
				continue
			}
			one := plan
			one.Passes = []tracking.Pass{pass}
			one.Start, one.End = recordingWindow(pass, s.opts.LeadSeconds, s.opts.TailSeconds)
			if elapsed(one.End, time.Now()) {
				// Fully-set passes are never planned; a pass in progress
				// (risen, not yet set) stays - it is an immediate chase.
				s.done[key] = true
				continue
			}
			candidates = append(candidates, candidate{sat: sat, plan: one, priority: priority, key: key, event: eventName})
			break // only the first open pass per satellite
		}
	}
	if len(candidates) == 0 {
		return nil, 15 * time.Minute
	}
	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if overlapping(a, b) && a.priority != b.priority {
			return a.priority < b.priority
		}
		return a.plan.Start.Before(b.plan.Start)
	})
	chosen := candidates[0]
	s.record(Status{Satellite: chosen.sat.Name, Receiver: chosen.plan.Receiver.URL,
		Rise: chosen.plan.Passes[0].Rise, Peak: chosen.plan.Passes[0].Peak.Time,
		MaxEl: chosen.plan.Passes[0].MaxElevation(), State: "planned"})
	return &chosen, 0
}

func overlapping(a, b candidate) bool {
	return a.plan.Start.Before(b.plan.End) && b.plan.Start.Before(a.plan.End)
}

// chaseAndDecode records one pass and decodes what the satellite sends.
func (s *Service) chaseAndDecode(ctx context.Context, c *candidate) {
	s.done[c.key] = true
	name := fmt.Sprintf("%s-%s.wav",
		strings.ReplaceAll(strings.ToLower(c.sat.Name), " ", ""),
		c.plan.Passes[0].Rise.UTC().Format("20060102T150405Z"))
	outPath := filepath.Join(s.opts.CapturesDir, name)
	if err := os.MkdirAll(s.opts.CapturesDir, 0o755); err != nil {
		s.fail(c, err)
		return
	}
	s.record(Status{Satellite: c.sat.Name, Receiver: c.plan.Receiver.URL,
		Rise: c.plan.Passes[0].Rise, Peak: c.plan.Passes[0].Peak.Time,
		MaxEl: c.plan.Passes[0].MaxElevation(), State: "chasing"})
	samples, err := satellite.Chase(ctx, c.plan, outPath, 150, 10*time.Second,
		satellite.SegmentSeconds, s.opts.Offline, s.say)
	if err != nil {
		s.fail(c, fmt.Errorf("chase: %w", err))
		return
	}
	s.say(fmt.Sprintf("%s: %d samples saved to %s", c.sat.Name, samples, outPath))
	detail := fmt.Sprintf("scheduled pass, peak %.0f deg", c.plan.Passes[0].MaxElevation())
	verified := false
	if c.sat.Group == "weather" {
		verified = s.decodeAPT(c, outPath)
	}
	if s.opts.Server != nil {
		_ = s.opts.Server.AddCaptureMetadataFull(name, web.CaptureMetadata{
			FrequencyHz: float64(c.sat.DownlinkHz), Mode: c.sat.Mode,
			Receiver: c.plan.Receiver.URL, Detail: detail, Kind: "satellite",
			Target: c.sat.Name, Verified: verified,
		})
	}
}

// decodeAPT runs the NOAA APT decoder on a captured pass and files the image
// with the web UI when it verifies.
func (s *Service) decodeAPT(c *candidate, wavPath string) bool {
	audio, rate, err := decode.ReadWAV(wavPath)
	if err != nil {
		s.fail(c, fmt.Errorf("read capture: %w", err))
		return false
	}
	result := decode.APTDecode(audio, rate)
	if result == nil {
		s.fail(c, fmt.Errorf("apt: no sync found"))
		return false
	}
	ok, why := result.Verified()
	s.say(fmt.Sprintf("%s: apt lines=%d locked=%d verified=%v (%s)",
		c.sat.Name, result.Lines, result.LinesLocked, ok, why))
	if !ok {
		s.record(Status{Satellite: c.sat.Name, Receiver: c.plan.Receiver.URL,
			Rise: c.plan.Passes[0].Rise, Peak: c.plan.Passes[0].Peak.Time,
			MaxEl: c.plan.Passes[0].MaxElevation(), State: "failed",
			Detail: "apt not verified: " + why})
		return false
	}
	png, err := result.PNG()
	if err != nil {
		s.fail(c, err)
		return false
	}
	if s.opts.Server != nil {
		activity := monitor.Activity{
			Started: c.plan.Passes[0].Rise, FrequencyHz: float64(c.sat.DownlinkHz),
			Kind: "data", Mode: "apt", Label: c.plan.Receiver.URL,
			Source: c.plan.Receiver.URL,
			Detail: fmt.Sprintf("%s APT via %s, %d lines (%d locked)",
				c.sat.Name, shortURL(c.plan.Receiver.URL), result.Lines, result.LinesLocked),
		}
		if err := s.opts.Server.AddLiveArtifact(activity, png); err != nil {
			s.say(fmt.Sprintf("gallery: %v", err))
		}
	}
	s.record(Status{Satellite: c.sat.Name, Receiver: c.plan.Receiver.URL,
		Rise: c.plan.Passes[0].Rise, Peak: c.plan.Passes[0].Peak.Time,
		MaxEl: c.plan.Passes[0].MaxElevation(), State: "decoded",
		Detail: fmt.Sprintf("apt %d lines", result.Lines)})
	return true
}

func (s *Service) fail(c *candidate, err error) {
	s.say(fmt.Sprintf("%s: %v", c.sat.Name, err))
	s.record(Status{Satellite: c.sat.Name, Receiver: c.plan.Receiver.URL,
		Rise: c.plan.Passes[0].Rise, Peak: c.plan.Passes[0].Peak.Time,
		MaxEl: c.plan.Passes[0].MaxElevation(), State: "failed", Detail: err.Error()})
}

func (s *Service) record(st Status) {
	s.status = append(s.status, st)
	if len(s.status) > 200 {
		s.status = s.status[len(s.status)-200:]
	}
}

func satByName(name string) (satellites.Sat, error) {
	for _, sat := range satellites.All() {
		if strings.EqualFold(sat.Name, name) {
			return sat, nil
		}
	}
	return satellites.Sat{}, fmt.Errorf("unknown satellite %q", name)
}

func shortURL(raw string) string {
	raw = strings.TrimPrefix(raw, "https://")
	raw = strings.TrimPrefix(raw, "http://")
	return raw
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
