package decode

// ACARS message semantics: interpreting the label and text of ACARS blocks
// carried over VHF (ARINC 618), VDL2 (ARINC 750/631), HFDL (ARINC 753/635)
// and Inmarsat/Iridium L-band links. Community networks such as airframes.io
// aggregate the RF-level blocks; this decoder turns the block's label and
// text into a human-readable interpretation.
//
// Label meanings follow the public ARINC 620 assignments; only widely
// documented labels are named, everything else is reported as unlisted.

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ACARSLabelDescriptions maps the two-character ACARS label to its
// ARINC 620 meaning for the labels that commonly appear on public feeds.
var ACARSLabelDescriptions = map[string]string{
	"Q0": "ACARS link test",
	"Q1": "ETA report",
	"Q2": "flight plan message",
	"Q5": "position report",
	"QA": "out of gate report",
	"QB": "off runway report",
	"QC": "on runway report",
	"QD": "in gate report",
	"QE": "departure clearance",
	"QF": "takeoff data / weight and balance",
	"H1": "ATS message (position, fuel, oceanic, CPDLC/ADS content)",
	"44": "OOOI / flight events",
	"45": "OOOI / flight events",
	"4A": "position report",
	"4J": "position report",
	"4M": "OOOI report",
	"5P": "position report",
	"5R": "position report",
	"5U": "weather request",
	"5Z": "airline-designated message",
	"80": "ADS-C / ATN address",
	"10": "airline operational control",
	"2L": "weather request",
	"2P": "weather request",
	"7A": "engine data",
	"7B": "engine data",
	"RA": "command/response",
	"SA": "media advisory",
	"SQ": "squitter / downlink status",
	"C1": "CPDLC uplink",
	"C2": "CPDLC downlink",
	"_ ": "no label",
}

// ACARSSemantic is the interpreted meaning of one ACARS text block.
type ACARSSemantic struct {
	Label       string   `json:"label,omitempty"`
	Meaning     string   `json:"meaning,omitempty"`
	Kind        string   `json:"kind"` // position, oooi, freetext, binary, empty
	Flight      string   `json:"flight,omitempty"`
	Lat         float64  `json:"lat,omitempty"`
	Lon         float64  `json:"lon,omitempty"`
	HasPos      bool     `json:"has_pos,omitempty"`
	Events      []string `json:"events,omitempty"` // OOOI events with times
	Notes       []string `json:"notes,omitempty"`
	Interpreted string   `json:"interpreted"`
}

var (
	reFlightFN  = regexp.MustCompile(`/FN\s*([A-Z0-9]{3,9})`)
	rePosDegMin = regexp.MustCompile(`([NS])(\d{2})(\d{2})\.?(\d?)\s*([EW])(\d{3})(\d{2})\.?(\d?)`)
	reOOOI      = regexp.MustCompile(`\b(OUT|OFF|ON|IN)\b\s*(\d{3,4})?`)
	reLongCoord = regexp.MustCompile(`([NS])(\d{4,5})([EW])(\d{5,6})`)
)

// InterpretACARS interprets one ACARS block: label semantics plus any
// structured content found in the text (position, OOOI events, flight id).
// The text of binary payloads (ATN/ADS-C) is reported as binary, not parsed.
func InterpretACARS(label, text string) *ACARSSemantic {
	s := &ACARSSemantic{Label: label, Kind: "empty"}
	if d, ok := ACARSLabelDescriptions[label]; ok {
		s.Meaning = d
	} else {
		s.Meaning = "unlisted label"
	}
	t := strings.TrimSpace(text)
	if t == "" {
		s.Interpreted = fmt.Sprintf("label %s (%s), empty text", label, s.Meaning)
		return s
	}
	printable := 0
	for i := 0; i < len(t); i++ {
		if t[i] >= 0x20 && t[i] <= 0x7e || t[i] == '\r' || t[i] == '\n' || t[i] == '\t' {
			printable++
		}
	}
	if printable*100/len(t) < 85 {
		s.Kind = "binary"
		s.Interpreted = fmt.Sprintf("label %s (%s), binary/encoded payload (%d bytes, likely ATN or ADS-C)", label, s.Meaning, len(t))
		return s
	}
	s.Kind = "freetext"
	if m := reFlightFN.FindStringSubmatch(t); m != nil {
		s.Flight = m[1]
	}
	if m := rePosDegMin.FindStringSubmatch(t); m != nil {
		lat := parseDegMin(m[1], m[2], m[3], m[4])
		lon := parseDegMin(m[5], m[6], m[7], m[8])
		s.Lat, s.Lon, s.HasPos = lat, lon, true
		s.Kind = "position"
	} else if m := reLongCoord.FindStringSubmatch(t); m != nil {
		s.Kind = "position"
		s.Notes = append(s.Notes, "compact position "+m[0])
	}
	var events []string
	for _, m := range reOOOI.FindAllStringSubmatch(t, -1) {
		ev := m[1]
		if m[2] != "" {
			ev += " " + m[2]
		}
		events = append(events, ev)
	}
	if len(events) > 0 {
		s.Events = events
		if s.Kind == "freetext" {
			s.Kind = "oooi"
		}
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "label %s (%s)", label, s.Meaning)
	if s.Flight != "" {
		fmt.Fprintf(&sb, " flight=%s", s.Flight)
	}
	if s.HasPos {
		fmt.Fprintf(&sb, " pos=%.4f,%.4f", s.Lat, s.Lon)
	}
	for _, e := range s.Events {
		fmt.Fprintf(&sb, " %s", e)
	}
	s.Interpreted = sb.String()
	return s
}

func parseDegMin(hemi, deg, min, frac string) float64 {
	di, _ := strconv.Atoi(deg)
	mi, _ := strconv.Atoi(min)
	fi, _ := strconv.Atoi(frac)
	d := float64(di) + float64(mi)/60 + float64(fi)/600
	if hemi == "S" || hemi == "W" {
		d = -d
	}
	return d
}
