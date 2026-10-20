// Code generated from sdrstream/satellites.py. DO NOT EDIT.

// Package satellites is the amateur-radio satellite catalogue.
package satellites

// Sat holds one satellite that transmits in an amateur band. Frequencies are
// in Hz. DownlinkHz is the frequency a receiver should tune to; UplinkHz is
// informational (this library only listens). ToneHz is the CTCSS tone a
// transmitter needs, which is irrelevant for receiving but useful context.
type Sat struct {
	Name       string
	NoradID    int
	DownlinkHz int64
	Mode       string
	UplinkHz   *int64
	ToneHz     *float64
	Status     string
	Notes      string
	Beacons    []int64
}

// Table lists the satellites most likely to be heard. Frequencies follow
// AMSAT's published coordination data; check the live TLE set for which are
// still transmitting.
var Table = []Sat{
	{Name: "ISS", NoradID: 25544, DownlinkHz: 145800000, Mode: "FM", UplinkHz: int64Ptr(145990000), ToneHz: float64Ptr(67.0), Status: "active", Notes: "Voice repeater (region 2/3 uplink); also SSTV and APRS.", Beacons: []int64{145825000, 145800000, 437800000}},
	{Name: "SO-50", NoradID: 27607, DownlinkHz: 436795000, Mode: "FM", UplinkHz: int64Ptr(145850000), ToneHz: float64Ptr(67.0), Status: "active", Notes: "Most reliable FM bird; 74.4 Hz tone to arm for 10 min.", Beacons: []int64{436795000}},
	{Name: "AO-91", NoradID: 43017, DownlinkHz: 145960000, Mode: "FM", UplinkHz: int64Ptr(435250000), ToneHz: float64Ptr(67.0), Status: "active", Notes: "Fox-1B. Downlink on 2 m so Doppler is mild.", Beacons: []int64{145960000}},
	{Name: "AO-92", NoradID: 43137, DownlinkHz: 145880000, Mode: "FM", UplinkHz: int64Ptr(435350000), ToneHz: float64Ptr(67.0), Status: "decayed", Notes: "Fox-1D. No current TLE; re-entered.", Beacons: []int64{145880000}},
	{Name: "AO-73", NoradID: 39444, DownlinkHz: 145960000, Mode: "USB", UplinkHz: int64Ptr(435150000), ToneHz: nil, Status: "active", Notes: "FUNcube-1 linear transponder; inverting. Telemetry 145.935.", Beacons: []int64{145935000, 145960000}},
	{Name: "RS-44", NoradID: 44909, DownlinkHz: 435620000, Mode: "USB", UplinkHz: int64Ptr(145965000), ToneHz: nil, Status: "active", Notes: "Linear transponder, inverting. Beacon near 435.610.", Beacons: []int64{435610000}},
	{Name: "CAS-4B", NoradID: 42759, DownlinkHz: 145925000, Mode: "USB", UplinkHz: int64Ptr(435280000), ToneHz: nil, Status: "decayed", Notes: "CAS-4B. No current TLE; re-entered.", Beacons: []int64{145925000}},
	{Name: "CAS-4A", NoradID: 42761, DownlinkHz: 145855000, Mode: "USB", UplinkHz: int64Ptr(435220000), ToneHz: nil, Status: "decayed", Notes: "CAS-4A. No current TLE; re-entered.", Beacons: []int64{145855000}},
	{Name: "PO-101", NoradID: 43678, DownlinkHz: 145900000, Mode: "FM", UplinkHz: int64Ptr(437500000), ToneHz: nil, Status: "active", Notes: "Diwata-2; FM voice repeater, scheduling varies.", Beacons: []int64{145900000}},
	{Name: "AO-7", NoradID: 7530, DownlinkHz: 145975000, Mode: "USB", UplinkHz: int64Ptr(432150000), ToneHz: nil, Status: "active", Notes: "Launched 1974, still working in sunlight. Mode B.", Beacons: []int64{145975000}},
	{Name: "AO-27", NoradID: 22825, DownlinkHz: 436795000, Mode: "FM", UplinkHz: int64Ptr(145850000), ToneHz: nil, Status: "active", Notes: "EYESAT A. FM repeater; intermittently active.", Beacons: []int64{436795000}},
	{Name: "FO-29", NoradID: 24278, DownlinkHz: 435800000, Mode: "USB", UplinkHz: int64Ptr(145950000), ToneHz: nil, Status: "active", Notes: "JAS-2. Linear transponder, inverting; long-lived. CW beacon 435.795.", Beacons: []int64{435795000}},
	{Name: "AO-85", NoradID: 40967, DownlinkHz: 145980000, Mode: "FM", UplinkHz: int64Ptr(435170000), ToneHz: float64Ptr(67.0), Status: "active", Notes: "Fox-1A. FM repeater, intermittent.", Beacons: []int64{145980000}},
	{Name: "AO-95", NoradID: 43770, DownlinkHz: 145915000, Mode: "FM", UplinkHz: int64Ptr(435300000), ToneHz: nil, Status: "active", Notes: "Fox-1Cliff. FM repeater.", Beacons: []int64{145915000}},
	{Name: "JO-97", NoradID: 43803, DownlinkHz: 145840000, Mode: "USB", UplinkHz: int64Ptr(435110000), ToneHz: nil, Status: "active", Notes: "JY1SAT. Linear transponder, inverting; also telemetry.", Beacons: []int64{145840000}},
	{Name: "CAS-9", NoradID: 50466, DownlinkHz: 145870000, Mode: "USB", UplinkHz: int64Ptr(435210000), ToneHz: nil, Status: "active", Notes: "XW-3. Linear transponder, inverting.", Beacons: []int64{145870000}},
	{Name: "IO-117", NoradID: 53109, DownlinkHz: 435310000, Mode: "USB", UplinkHz: int64Ptr(145925000), ToneHz: nil, Status: "active", Notes: "GreenCube. 9k6 digipeater; downlink 435.310.", Beacons: []int64{435310000}},
	{Name: "AO-123", NoradID: 61781, DownlinkHz: 435400000, Mode: "FM", UplinkHz: int64Ptr(145850000), ToneHz: float64Ptr(67.0), Status: "active", Notes: "ASRTU-1. FM repeater.", Beacons: []int64{435400000}},
	{Name: "SONATE-2", NoradID: 59112, DownlinkHz: 145880000, Mode: "FM", UplinkHz: int64Ptr(435950000), ToneHz: nil, Status: "active", Notes: "FM repeater and APRS digipeater.", Beacons: []int64{145880000}},
}

func int64Ptr(value int64) *int64 { return &value }

func float64Ptr(value float64) *float64 { return &value }
