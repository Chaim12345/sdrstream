package monitor

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Channel is one fixed voice frequency the broadcast strategy visits.
type Channel struct {
	FrequencyHz float64 `json:"frequency_hz"`
	Mode        string  `json:"mode"`  // am, nfm, fm
	Label       string  `json:"label"` // what the channel is, e.g. "Marine VHF ch16"
}

// DefaultVoiceChannels are well-known terrestrial voice channels in
// VHF/UHF. None of them is a satellite downlink. They are starting points,
// not a claim that anything is on the air: the monitor only reports what it
// actually hears.
func DefaultVoiceChannels() []Channel {
	return []Channel{
		{121500000, "am", "Airband guard 121.5"},
		{118000000, "am", "Airband 118.0"},
		{123450000, "am", "Airband air-to-air 123.45"},
		{156800000, "nfm", "Marine VHF ch16 distress/calling"},
		{156300000, "nfm", "Marine VHF ch6 safety"},
		{146520000, "nfm", "2 m amateur calling 146.52"},
		{145500000, "nfm", "2 m amateur 145.5"},
		{446000000, "nfm", "70 cm PMR446 / amateur calling 446.0"},
		{433500000, "nfm", "70 cm amateur 433.5"},
	}
}

// ParseChannels reads "freqHz:mode:label" entries separated by commas or
// newlines; mode and label are optional (mode defaults to nfm).
func ParseChannels(spec string) ([]Channel, error) {
	var out []Channel
	for _, entry := range strings.FieldsFunc(spec, func(r rune) bool { return r == ',' || r == '\n' }) {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parts := strings.SplitN(entry, ":", 3)
		var hz float64
		if _, err := fmt.Sscanf(parts[0], "%g", &hz); err != nil || hz < 1e6 {
			return nil, fmt.Errorf("monitor: bad channel frequency %q", parts[0])
		}
		c := Channel{FrequencyHz: hz, Mode: "nfm"}
		if len(parts) > 1 && strings.TrimSpace(parts[1]) != "" {
			c.Mode = strings.ToLower(strings.TrimSpace(parts[1]))
		}
		if len(parts) > 2 {
			c.Label = strings.TrimSpace(parts[2])
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("monitor: no channels in %q", spec)
	}
	return out, nil
}

// ChannelStatus is the honest per-channel state of the broadcast rotation.
type ChannelStatus struct {
	Channel Channel   `json:"channel"`
	Visits  int       `json:"visits"`
	Voice   int       `json:"voice_hits"`
	Last    time.Time `json:"last_visit,omitempty"`
	Status  string    `json:"status"`
}

// BroadcastStatus summarises the rotation without claiming more than was
// observed.
type BroadcastStatus struct {
	Running  bool            `json:"running"`
	Channels []ChannelStatus `json:"channels"`
	Summary  string          `json:"summary"`
}

type broadcastState struct {
	mu    sync.Mutex
	stats map[int]*ChannelStatus
	order []Channel
	run   bool
}

// Status reports the broadcast rotation. Strings are factual: "not yet
// visited", "quiet at last visit", "voice heard, not transcribed",
// "voice heard, transcript rejected by quality gate", "transcribed".
func (m *Monitor) Status() BroadcastStatus {
	m.bc.mu.Lock()
	defer m.bc.mu.Unlock()
	st := BroadcastStatus{Running: m.bc.run}
	voice, visited := 0, 0
	for i, c := range m.bc.order {
		cs := ChannelStatus{Channel: c, Status: "not yet visited"}
		if s := m.bc.stats[i]; s != nil {
			cs = *s
		}
		if cs.Visits > 0 {
			visited++
		}
		voice += cs.Voice
		st.Channels = append(st.Channels, cs)
	}
	sort.SliceStable(st.Channels, func(i, j int) bool { return st.Channels[i].Channel.FrequencyHz < st.Channels[j].Channel.FrequencyHz })
	st.Summary = fmt.Sprintf("%d of %d channels visited, %d voice detections", visited, len(st.Channels), voice)
	return st
}

func (m *Monitor) noteVisit(i int, c Channel, kind, detail string) {
	m.bc.mu.Lock()
	defer m.bc.mu.Unlock()
	if m.bc.stats == nil {
		m.bc.stats = map[int]*ChannelStatus{}
	}
	s := m.bc.stats[i]
	if s == nil {
		s = &ChannelStatus{Channel: c}
		m.bc.stats[i] = s
	}
	s.Visits++
	s.Last = time.Now()
	switch {
	case kind == "voice" && detail != "":
		s.Voice++
		s.Status = "transcribed"
	case kind == "voice":
		s.Voice++
		s.Status = "voice heard, no accepted transcript"
	case kind == "":
		s.Status = "no audio from receiver"
	case kind == "noise" || kind == "silence" || kind == "unknown":
		s.Status = "quiet at last visit"
	default:
		s.Status = kind + " heard"
	}
}

// runChannels rotates over the configured channels one at a time. One
// channel is recorded per Listen+Settle period, which bounds both the
// receiver connection and the whisper load to a single clip at once; the
// per-visit Pause lets the load guard and the worker keep up.
func (m *Monitor) runChannels(ctx context.Context) error {
	channels := m.opts.Channels
	m.bc.mu.Lock()
	m.bc.order = channels
	m.bc.run = true
	m.bc.mu.Unlock()
	defer func() { m.bc.mu.Lock(); m.bc.run = false; m.bc.mu.Unlock() }()
	if m.opts.Listen <= 0 {
		return fmt.Errorf("monitor: broadcast channels need a listen duration")
	}
	pause := m.opts.ChannelPause
	for i := 0; ; i = (i + 1) % len(channels) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		ch := channels[i]
		kind, detail := m.visitChannel(ctx, ch)
		m.noteVisit(i, ch, kind, detail)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pause):
		}
	}
}

// visitChannel records and classifies one clip synchronously, so only one
// transcription runs at a time. Noise and silence never become activities.
func (m *Monitor) visitChannel(ctx context.Context, ch Channel) (kind, detail string) {
	mode := ch.Mode
	if mode == "" {
		mode = "nfm"
	}
	samples, rate, err := m.opts.Source.Listen(ctx, ch.FrequencyHz, mode, m.opts.Listen)
	listenEnd := time.Now()
	if err != nil || len(samples) == 0 || rate <= 0 {
		if err != nil {
			m.opts.Logf("channel %.4f MHz: listen failed: %v", ch.FrequencyHz/1e6, err)
		}
		return "", ""
	}
	kind, modeName, detail, _ := m.classify(ctx, samples, rate, 0)
	if kind == "noise" || kind == "silence" || kind == "unknown" {
		return kind, ""
	}
	if modeName == "" {
		modeName = mode
	}
	m.mu.Lock()
	m.nextID++
	a := Activity{
		ID: m.nextID, Started: listenEnd.Add(-time.Duration(float64(len(samples)) / float64(rate) * float64(time.Second))),
		Ended: &listenEnd, FrequencyHz: ch.FrequencyHz, Kind: kind, Mode: modeName, Detail: detail,
		Source: m.opts.Name, Label: ch.Label, DurationSec: float64(len(samples)) / float64(rate),
	}
	if a.Label == "" {
		a.Label = m.opts.Label
	}
	m.activities = append(m.activities, a)
	if len(m.activities) > 200 {
		m.activities = m.activities[len(m.activities)-200:]
	}
	m.mu.Unlock()
	m.publish(a)
	return kind, detail
}
