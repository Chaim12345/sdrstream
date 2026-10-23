package satellites

import (
	"strings"
	"time"
)

// Event is a dated transmission event that temporarily overrides a
// satellite's usual downlink plan, such as an ARISS SSTV series. Event
// entries are added only from current, dated source evidence and carry
// their source; remove them when they expire.
type Event struct {
	Satellite  string // catalogue satellite name, e.g. "ISS"
	Name       string // event name, e.g. "ARISS SSTV Series 34"
	StartUTC   string // inclusive start, RFC3339 or dated text
	EndUTC     string // inclusive end
	DownlinkHz int64  // event downlink
	Mode       string // e.g. "FM"
	ImageMode  string // e.g. "Robot36" for SSTV events
	Callsign   string
	Source     string // dated source URL the entry was taken from
	Notes      string
}

// Events lists known current and upcoming transmission events.
var Events = []Event{
	{
		Satellite: "ISS",
		Name:      "ARISS SSTV Series 34 (ARISS Hardware)",
		StartUTC:  "2026-10-22T15:30:00Z",
		// End resolved from the owning source 2026-10-23: the ARISS
		// announcement (@arissintl.bsky.social, 2026-10-14, republished
		// verbatim by sbarc.co.uk) states Start 15:30 UTC 22/10, End
		// 17:45 UTC 26/10; its 2026-10-21 update EXTENDED the end to
		// that time. Secondary "Oct 22-25" renderings (slowscantv.com
		// title, AMSAT "concludes Oct 25 TBD") are lossy summaries of
		// the same post, not a disagreement. Dates remain subject to
		// ISS operational changes per the announcement.
		EndUTC:     "2026-10-26T17:45:00Z",
		DownlinkHz: 437550000,
		Mode:       "FM",
		ImageMode:  "Robot36",
		Callsign:   "RS0ISS",
		Source:     "Owning source: ARISS @arissintl.bsky.social 2026-10-14 post, verbatim republication https://www.sbarc.co.uk/news/expedition-75-series-34-sstv-from-the-iss/ (updates 2026-10-20 times confirmed, 2026-10-21 end extended to Oct 26 17:45 UTC). Corroborated by darc.de 2026-10-20, darc-e39.org 2026-10-21, f6kmf.fr 2026-10-15. Verified 2026-10-23.",
		Notes: "36 s on, 2 min off; allow +/-10 kHz Doppler. After the event the Service Module " +
			"transceiver stays on 437.825 MHz APRS until end of 2026. Series 34 uses 70cm, not the " +
			"legacy 145.800 MHz downlink.",
	},
}

// ActiveEvent returns the dated event overriding the satellite's downlink
// plan at time at, or nil. Window bounds are inclusive; entries whose
// dates do not parse are skipped rather than trusted.
func ActiveEvent(name string, at time.Time) *Event {
	for i := range Events {
		ev := &Events[i]
		if !strings.EqualFold(strings.TrimSpace(ev.Satellite), strings.TrimSpace(name)) {
			continue
		}
		start, err1 := time.Parse(time.RFC3339, ev.StartUTC)
		end, err2 := time.Parse(time.RFC3339, ev.EndUTC)
		if err1 != nil || err2 != nil {
			continue
		}
		if !at.Before(start) && !at.After(end) {
			return ev
		}
	}
	return nil
}

// EventCovering returns the dated event whose window intersects
// [start, end], or nil. Used at planning time: an event starting inside
// the lookahead window must already drive frequency and receiver choice.
func EventCovering(name string, start, end time.Time) *Event {
	for i := range Events {
		ev := &Events[i]
		if !strings.EqualFold(strings.TrimSpace(ev.Satellite), strings.TrimSpace(name)) {
			continue
		}
		evStart, err1 := time.Parse(time.RFC3339, ev.StartUTC)
		evEnd, err2 := time.Parse(time.RFC3339, ev.EndUTC)
		if err1 != nil || err2 != nil {
			continue
		}
		if evStart.Before(end) && start.Before(evEnd) {
			return ev
		}
	}
	return nil
}
