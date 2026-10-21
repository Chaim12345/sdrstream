// Package relays identifies the amateur satellites that carry a repeater,
// transponder or digipeater, as opposed to beacon-only spacecraft. For a
// listening station the relay downlink is the interesting signal: whatever
// the uplink carries is repeated on it.
package relays

import (
	"sort"
	"strings"

	"github.com/Chaim12345/sdrstream/go/internal/satellites"
)

// Kind is how a satellite relays traffic.
type Kind string

const (
	FMVoice     Kind = "FM voice"
	Linear      Kind = "linear transponder"
	Digipeater  Kind = "digipeater"
	Transponder Kind = "transponder"
)

// Relay is an active satellite with an uplink.
type Relay struct {
	Sat  satellites.Sat
	Kind Kind
}

// KindOf classifies a satellite from its mode and notes.
func KindOf(sat satellites.Sat) Kind {
	notes := strings.ToLower(sat.Notes)
	switch {
	case strings.Contains(notes, "digipeater"):
		return Digipeater
	case sat.Mode == "FM":
		return FMVoice
	case sat.Mode == "USB", sat.Mode == "LSB", sat.Mode == "CW":
		return Linear
	default:
		return Transponder
	}
}

// IsRelay reports whether the satellite has an uplink, which is the only
// thing a listening station can use to tell repeaters from beacons.
func IsRelay(sat satellites.Sat) bool {
	return sat.UplinkHz != nil
}

// Table returns the relay satellites, active first and then by name.
func Table(includeInactive bool) []Relay {
	var out []Relay
	for _, sat := range satellites.Table {
		if !IsRelay(sat) || (sat.Status != "active" && !includeInactive) {
			continue
		}
		out = append(out, Relay{Sat: sat, Kind: KindOf(sat)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Sat.Status != out[j].Sat.Status {
			return out[i].Sat.Status == "active"
		}
		return out[i].Sat.Name < out[j].Sat.Name
	})
	return out
}
