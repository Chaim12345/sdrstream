package monitor

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestParseChannels(t *testing.T) {
	got, err := ParseChannels("121500000:am:Guard, 156.8e6, 146520000:nfm")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Mode != "am" || got[0].Label != "Guard" || got[1].Mode != "nfm" || got[1].FrequencyHz != 156.8e6 {
		t.Fatalf("parsed %+v", got)
	}
	for _, bad := range []string{"", "abc", "5"} {
		if _, err := ParseChannels(bad); err == nil {
			t.Errorf("ParseChannels(%q) succeeded, want error", bad)
		}
	}
}

func TestDefaultChannelsAreTerrestrialVHFUHF(t *testing.T) {
	for _, c := range DefaultVoiceChannels() {
		if c.FrequencyHz < 108e6 || c.FrequencyHz > 470e6 || c.Label == "" || c.Mode == "" {
			t.Errorf("bad default channel %+v", c)
		}
	}
}

// A rotation over quiet channels visits each in order with the right mode,
// publishes nothing, and says so honestly.
func TestBroadcastRotationQuietChannels(t *testing.T) {
	src := &fakeSource{level: map[float64]float64{}}
	chans := []Channel{{121.5e6, "am", "Guard"}, {156.8e6, "nfm", "Ch16"}}
	published := 0
	m := New(Options{Source: src, Strategy: "broadcast", Channels: chans, Listen: time.Second,
		ChannelPause: time.Millisecond, OnActivity: func(Activity) { published++ }})
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	m.Run(ctx)
	src.mu.Lock()
	defer src.mu.Unlock()
	if len(src.freqs) < 2 || src.freqs[0] != 121.5e6 || src.freqs[1] != 156.8e6 {
		t.Fatalf("visit order = %v", src.freqs)
	}
	if src.modes[0] != "am" || src.modes[1] != "nfm" {
		t.Fatalf("modes = %v", src.modes)
	}
	if published != 0 || len(m.Activities()) != 0 {
		t.Fatalf("silence produced %d activities", published)
	}
	st := m.Status()
	if st.Running || len(st.Channels) != 2 {
		t.Fatalf("status = %+v", st)
	}
	for _, c := range st.Channels {
		if c.Visits == 0 || c.Status != "quiet at last visit" {
			t.Errorf("channel %+v, want quiet at last visit", c)
		}
	}
	if !strings.Contains(st.Summary, "2 of 2 channels visited, 0 voice") {
		t.Errorf("summary = %q", st.Summary)
	}
}

func TestBroadcastStatusStrings(t *testing.T) {
	c := Channel{146.52e6, "nfm", "x"}
	m := New(Options{Channels: []Channel{c}})
	m.bc.order = m.opts.Channels
	if s := m.Status(); s.Channels[0].Status != "not yet visited" {
		t.Fatalf("%+v", s)
	}
	m.noteVisit(0, c, "voice", "")
	if s := m.Status().Channels[0]; s.Status != "voice heard, no accepted transcript" || s.Voice != 1 {
		t.Fatalf("%+v", s)
	}
	m.noteVisit(0, c, "voice", "mayday")
	if s := m.Status().Channels[0]; s.Status != "transcribed" {
		t.Fatalf("%+v", s)
	}
	m.noteVisit(0, c, "", "")
	if s := m.Status().Channels[0]; s.Status != "no audio from receiver" {
		t.Fatalf("%+v", s)
	}
}

func TestBroadcastNeedsListen(t *testing.T) {
	m := New(Options{Source: &fakeSource{}, Strategy: "broadcast", Channels: DefaultVoiceChannels()})
	if err := m.runChannels(context.Background()); err == nil {
		t.Fatal("want error with Listen=0")
	}
}
