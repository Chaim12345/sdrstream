// Package military lists government and military spacecraft with published
// downlinks that a public receiver might reach, and reports which catalogue
// receivers can hear them.
//
// Three tiers of practical interest: open legacy systems (FLTSATCOM-era UHF
// repeaters that relay whatever reaches them), encrypted beacons (carrier and
// Doppler curve only) and out-of-reach systems (EHF/X-band, needing dishes).
package military

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Chaim12345/sdrstream/go/internal/catalogue"
)

// UHF SATCOM occupies 240-270 MHz. The amateur 70 cm band stops at 440 MHz,
// so receivers tagged for it do not automatically reach this.
const (
	UHSatcomLowHz  = 243_000_000
	UHSatcomHighHz = 270_000_000
)

// Downlink is one frequency/mode pair.
type Downlink struct {
	FrequencyHz int64
	Mode        string
}

// GovernmentSatellite is a government or military spacecraft with a published
// downlink.
type GovernmentSatellite struct {
	Name       string
	Operator   string
	DownlinkHz int64
	Mode       string
	Category   string // "comms", "weather", "early-warning", "navigation", "reconnaissance"
	Encryption string // "open", "encrypted" or "beacon"
	Notes      string
	Source     string
	NoradID    int
	ExtraDownlinks []Downlink
	Constellation  bool
}

// Band is a coarse band name matching the vocabulary used elsewhere.
func (g GovernmentSatellite) Band() string {
	switch {
	case g.DownlinkHz >= 136_000_000 && g.DownlinkHz <= 138_000_000:
		return "137 MHz"
	case g.DownlinkHz >= 148_000_000 && g.DownlinkHz <= 152_000_000:
		return "150 MHz"
	case g.DownlinkHz >= UHSatcomLowHz && g.DownlinkHz <= UHSatcomHighHz:
		return "UHF SATCOM"
	case g.DownlinkHz >= 380_000_000 && g.DownlinkHz <= 480_000_000:
		return "400-470 MHz"
	case g.DownlinkHz >= 1_500_000_000 && g.DownlinkHz <= 1_600_000_000:
		return "L-band"
	case g.DownlinkHz >= 2_200_000_000 && g.DownlinkHz <= 2_300_000_000:
		return "S-band"
	}
	return "other"
}

// IsOpen reports whether any downlink carries unencrypted traffic.
func (g GovernmentSatellite) IsOpen() bool { return g.Encryption == "open" }

// AllDownlinks returns every downlink, primary first.
func (g GovernmentSatellite) AllDownlinks() []Downlink {
	out := make([]Downlink, 0, len(g.ExtraDownlinks)+1)
	out = append(out, Downlink{FrequencyHz: g.DownlinkHz, Mode: g.Mode})
	return append(out, g.ExtraDownlinks...)
}

// Summary renders one line for the report.
func (g GovernmentSatellite) Summary() string {
	return fmt.Sprintf("%-22s %9.4f MHz %-4s %-11s %s",
		g.Name, float64(g.DownlinkHz)/1e6, g.Mode, g.Band(), g.Encryption)
}

// Table is ordered roughly by how rewarding reception is: the open legacy
// systems first.
var Table = []GovernmentSatellite{
	{
		Name: "FLTSATCOM", Operator: "US Navy", DownlinkHz: 255_550_000, Mode: "NFM",
		Category: "comms", Encryption: "open",
		Notes: "1970s-80s UHF repeater constellation. Built before practical " +
			"encryption, so transponders still relay whatever reaches them, " +
			"which in practice is often unlicensed third-party traffic. The most " +
			"widely heard military downlink in the world. 255.550 is the famous " +
			"channel; a 25 kHz transponder sits on FLTSATCOM 8 at 15W.",
		Source: "mattblaze.org/misc/uhf-sats",
		ExtraDownlinks: []Downlink{{255_250_000, "NFM"}, {253_850_000, "NFM"}},
		Constellation:  true,
	},
	{
		Name: "UFO (UHF Follow-On)", Operator: "US Navy", DownlinkHz: 255_550_000, Mode: "NFM",
		Category: "comms", Encryption: "open",
		Notes: "Successor to FLTSATCOM and still the backbone of US UHF SATCOM. The " +
			"narrowband channels carry 25 or 30 kHz NFM and several are heard " +
			"carrying open traffic. Channel 20 on UFO6 (105.5W) is the best " +
			"documented. Geostationary, so it is audible continuously rather than " +
			"only during a pass.",
		Source:         "mattblaze.org/misc/uhf-sats",
		ExtraDownlinks: []Downlink{{255_350_000, "NFM"}, {253_650_000, "NFM"}},
		Constellation:  true,
	},
	{
		Name: "Skynet", Operator: "UK MoD", DownlinkHz: 255_550_000, Mode: "NFM",
		Category: "comms", Encryption: "open",
		Notes: "British equivalent of FLTSATCOM, sharing the same 240-270 MHz band. " +
			"The documented UK channels sit near 254.075 and 254.150 MHz. Skynet " +
			"4 and 5 are the older spacecraft still carrying legacy UHF.",
		Source:        "uhf-satcom.com/satellite-reception/uhf",
		Constellation: true,
	},
	{
		Name: "Gonets-M / Strela-3", Operator: "Roscosmos / Russian military",
		DownlinkHz: 260_000_000, Mode: "FM",
		Category: "comms", Encryption: "open",
		Notes: "Russian store-and-dump messaging constellation at about 1400 km, " +
			"inclined 82.5 degrees. Downlinks in 259.5-265.2 MHz and 387-390 MHz " +
			"as short digital bursts, 300-500 ms when no ground station is in " +
			"range. Because the orbit is low, passes are frequent and brief, and " +
			"the burst timing is a neat way to confirm a pass prediction.",
		Source:         "uhf-satcom.com/satellite-reception/uhf",
		ExtraDownlinks: []Downlink{{388_000_000, "FM"}},
		Constellation:  true,
	},
	{
		Name: "Meridian", Operator: "Russian military", DownlinkHz: 260_000_000, Mode: "FM",
		Category: "comms", Encryption: "open",
		Notes: "Russian military communications satellites in Molniya-type highly " +
			"elliptical orbits, the replacement for the Molniya series. Downlinks " +
			"at 259.5-265.2 MHz, 387-390 MHz and 1541.0-1541.9 MHz. The " +
			"high-latitude apogee means long, high-elevation passes from northern " +
			"receivers.",
		Source:         "uhf-satcom.com/satellite-reception/uhf",
		ExtraDownlinks: []Downlink{{388_000_000, "FM"}, {1_541_000_000, "FM"}},
		Constellation:  true,
	},
	{
		Name: "DMSP", Operator: "US Space Force", DownlinkHz: 400_032_000, Mode: "FM",
		Category: "weather", Encryption: "encrypted",
		Notes: "Defense Meteorological Satellite Program, the military weather " +
			"constellation flown since the 1960s. The UHF real-time data stream " +
			"runs at 400.032 and 400.822 MHz and the S-band links at 2222.5 and " +
			"2237.5 MHz. The payload is encrypted, so reception shows the carrier " +
			"and its Doppler curve and nothing more. Still a good way to test a " +
			"400 MHz receive chain.",
		Source: "eoportal.org/satellite-missions/dmsp-block-5d", NoradID: 25991,
		ExtraDownlinks: []Downlink{{400_822_000, "FM"}, {2_237_500_000, "FM"}},
		Constellation:  true,
	},
	{
		Name: "Molniya", Operator: "Russian military", DownlinkHz: 1_000_450_000, Mode: "FM",
		Category: "comms", Encryption: "encrypted",
		Notes: "The original Soviet military communications series, in highly " +
			"elliptical 12-hour orbits that give long dwell times at high " +
			"latitude. Reported UHF downlinks near 1000.45 and 992.45 MHz, with " +
			"direct-sequence spread spectrum at 990.6 MHz. The spread-spectrum " +
			"links are effectively unreadable without the spreading code.",
		Source:        "uhf-satcom.com/satellite-reception/uhf",
		Constellation: true,
	},
	{
		Name: "DSP / SBIRS", Operator: "US Space Force", DownlinkHz: 2_237_500_000, Mode: "FM",
		Category: "early-warning", Encryption: "encrypted",
		Notes: "Missile early-warning constellations, geostationary and highly " +
			"inclined. They transmit SGLS telemetry and command links in the " +
			"2200-2300 MHz range, with carriers reported near 2237.5 and " +
			"2252.5 MHz. The links are encrypted and use spread spectrum; a " +
			"receiver sees a carrier, not a message.",
		Source:        "orbitalfocus.uk/Frequencies/FrequenciesAll.php",
		Constellation: true,
	},
	{
		Name: "NOSS / Intruder", Operator: "US Navy", DownlinkHz: 400_000_000, Mode: "FM",
		Category: "reconnaissance", Encryption: "encrypted",
		Notes: "Naval ocean surveillance satellites that fly in tight triplets and " +
			"locate ships by their radio emissions. Downlinks have been reported " +
			"around 400 MHz. There is no open payload, and the formations are " +
			"easier to see in the pass timings than in the signal.",
		Source:        "uhf-satcom.com/satellite-reception/uhf",
		Constellation: true,
	},
}

// OutOfReach belongs on a "military" list but no public SDR can reach it: EHF
// and X-band need dishes and hardware the receivers here do not have.
var OutOfReach = map[string]string{
	"Milstar / AEHF":       "20/44 GHz EHF; needs a dish and an EHF terminal.",
	"Lacrosse / Onyx":      "X-band radar imaging, around 9.6 GHz; dish required.",
	"KH-11 (Crystal)":      "Data relayed through other spacecraft, not broadcast.",
	"GPS (military M-code)": "M-code is encrypted and on a different signal " +
		"from the open L1/L2 C/A that civil receivers use.",
}

// Reachability pairs a satellite with the receivers that cover it.
type Reachability struct {
	Satellite GovernmentSatellite
	Receivers []catalogue.Receiver
}

// Reachable returns, for each satellite, the catalogue receivers that can
// hear its downlinks.
func Reachable(receivers []catalogue.Receiver, table []GovernmentSatellite) []Reachability {
	if table == nil {
		table = Table
	}
	out := make([]Reachability, 0, len(table))
	for _, satellite := range table {
		var heard []catalogue.Receiver
		for _, receiver := range receivers {
			for _, downlink := range satellite.AllDownlinks() {
				if receiver.CanHear(float64(downlink.FrequencyHz)) {
					heard = append(heard, receiver)
					break
				}
			}
		}
		out = append(out, Reachability{Satellite: satellite, Receivers: heard})
	}
	return out
}

// ByEncryption filters the table to one encryption tier.
func ByEncryption(level string) []GovernmentSatellite {
	var out []GovernmentSatellite
	for _, satellite := range Table {
		if satellite.Encryption == level {
			out = append(out, satellite)
		}
	}
	return out
}

// ByCategory filters the table to one category.
func ByCategory(category string) []GovernmentSatellite {
	var out []GovernmentSatellite
	for _, satellite := range Table {
		if satellite.Category == category {
			out = append(out, satellite)
		}
	}
	return out
}

// Report renders the reachability report. With no receivers passed, the
// bundled catalogue is used.
func Report(receivers []catalogue.Receiver) string {
	if receivers == nil {
		for _, receiver := range catalogue.Table {
			receivers = append(receivers, receiver)
		}
	}
	var lines []string
	reachable := Reachable(receivers, nil)
	for _, entry := range reachable {
		satellite := entry.Satellite
		lines = append(lines, satellite.Summary())
		if len(entry.Receivers) == 0 {
			lines = append(lines, "    no catalogue receiver covers this downlink")
			continue
		}
		for _, receiver := range entry.Receivers {
			lines = append(lines, fmt.Sprintf("    %-46s (%8.4f,%9.4f)",
				receiver.URL, receiver.Latitude, receiver.Longitude))
		}
	}
	lines = append(lines, "")
	lines = append(lines, "out of reach of any public receiver:")
	names := make([]string, 0, len(OutOfReach))
	for name := range OutOfReach {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		lines = append(lines, fmt.Sprintf("  %-24s %s", name, OutOfReach[name]))
	}
	return strings.Join(lines, "\n")
}
