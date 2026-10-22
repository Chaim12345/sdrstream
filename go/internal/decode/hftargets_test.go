package decode

import (
	"context"
	"testing"
	"time"
)

// The restored non-satellite HF monitoring list: broadcast, utility,
// NAVTEX, time signals and weather fax/RTTY, plus the Shannon VOLMET voice
// sample. Exactly 45 targets.
func TestHFBroadcastsRestoredList(t *testing.T) {
	if len(HFBroadcasts) != 45 {
		t.Fatalf("HFBroadcasts has %d targets, want 45", len(HFBroadcasts))
	}
	if HFBroadcasts[0].Name != "Shannon VOLMET 5505" || HFBroadcasts[0].Kind != "voice" {
		t.Fatalf("first target = %q (%s), want the Shannon VOLMET voice sample", HFBroadcasts[0].Name, HFBroadcasts[0].Kind)
	}
	seen := map[string]bool{}
	knownKinds := map[string]bool{
		"rtty": true, "cw": true, "navtex": true, "wefax": true,
		"psk31": true, "ft8": true, "wspr": true, "dsc": true,
		"hfdl": true, "voice": true,
	}
	for _, tgt := range HFBroadcasts {
		if tgt.Name == "" {
			t.Error("target with empty Name")
		}
		if seen[tgt.Name] {
			t.Errorf("duplicate target %q", tgt.Name)
		}
		seen[tgt.Name] = true
		if !knownKinds[tgt.Kind] {
			t.Errorf("%s: unknown kind %q", tgt.Name, tgt.Kind)
		}
		if tgt.CarrierKHz <= 0 || tgt.DialKHz <= 0 {
			t.Errorf("%s: non-positive frequency", tgt.Name)
		}
		if tgt.Mode == "" {
			t.Errorf("%s: empty mode", tgt.Name)
		}
		switch tgt.Kind {
		case "rtty", "navtex":
			if tgt.Baud <= 0 || tgt.MarkHz <= 0 || tgt.SpaceHz <= 0 {
				t.Errorf("%s: %s target missing baud/mark/space", tgt.Name, tgt.Kind)
			}
		case "wefax":
			if tgt.LinesPerMin <= 0 || tgt.IOC <= 0 {
				t.Errorf("%s: wefax target missing drum speed/IOC", tgt.Name)
			}
		}
	}
}

// Pins the DWD RTTY 4583 kHz path verified live on 2026-10-21
// ("CQ CQ CQ DE DDK2 DDH7 DDK9"): USB dial 1.9 kHz below centre, 50 baud,
// mark low (inverted), DDK2 anchor.
func TestDWDRTTY4583VerifiedPath(t *testing.T) {
	var tgt *HFTarget
	for i := range HFBroadcasts {
		if HFBroadcasts[i].Name == "DWD RTTY 4583 (night)" {
			tgt = &HFBroadcasts[i]
		}
	}
	if tgt == nil {
		t.Fatal("verified DWD RTTY 4583 target missing")
	}
	if tgt.Kind != "rtty" || tgt.CarrierKHz != 4583.0 || tgt.DialKHz != 4581.1 || tgt.Mode != "usb" {
		t.Errorf("dial path changed: %+v", tgt)
	}
	if tgt.Baud != 50 || tgt.MarkHz != 1675 || tgt.SpaceHz != 2125 || !tgt.Inverted {
		t.Errorf("tone path changed: %+v", tgt)
	}
	if tgt.Expect != "DDK2" {
		t.Errorf("verify anchor changed: %q", tgt.Expect)
	}
}

// DWD fax schedule handling: ActiveNow gates wefax targets to the DWD
// broadcast windows; 24/7 services are always eligible.
func TestActiveNowDWDSchedule(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2026, 10, 22, h, m, 0, 0, time.UTC) }
	wefax := HFTarget{Kind: "wefax"}
	cases := []struct {
		h, m int
		want bool
	}{
		{5, 59, false}, {6, 0, true}, {12, 30, true}, {19, 39, true},
		{19, 40, false}, {20, 59, false}, {21, 0, true}, {23, 59, true},
		{0, 30, false},
	}
	for _, c := range cases {
		if got := wefax.ActiveNow(at(c.h, c.m)); got != c.want {
			t.Errorf("wefax ActiveNow(%02d:%02d UTC) = %v, want %v", c.h, c.m, got, c.want)
		}
	}
	for _, kind := range []string{"rtty", "cw", "navtex"} {
		if !((HFTarget{Kind: kind}).ActiveNow(at(3, 15))) {
			t.Errorf("%s should be active around the clock", kind)
		}
	}
}

// Targets without a decoder must still fail the gate, never publish.
func TestDecodeHFNoBackendKindsNeverLive(t *testing.T) {
	silence := make([]int16, 2400)
	for _, kind := range []string{"voice", "dsc", "hfdl", "ft8", "wspr", "psk31"} {
		r := decodeHF(context.Background(), HFTarget{Name: "t", Kind: kind}, "test", silence, 12000)
		if r.Gate.Live {
			t.Errorf("kind %s passed the gate without a decoder", kind)
		}
	}
}

func TestDigitalTargetsRequestLongEnoughListens(t *testing.T) {
	for _, tg := range HFBroadcasts {
		switch tg.Kind {
		case "wspr":
			if tg.ListenFor(12*time.Second) < 240*time.Second {
				t.Errorf("%s listens %v, WSPR needs a full window", tg.Name, tg.ListenFor(12*time.Second))
			}
		case "ft8":
			if tg.ListenFor(12*time.Second) < 35*time.Second {
				t.Errorf("%s listens %v, FT8/FT4 need two slot boundaries", tg.Name, tg.ListenFor(12*time.Second))
			}
		}
	}
}
