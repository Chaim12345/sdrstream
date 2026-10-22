package decode

import "time"

// HFTarget is a known HF broadcast service that can be monitored on a
// public receiver: when it transmits, where to point the dial, how the
// audio should be demodulated, and which decoder turns it back into text
// or images.
type HFTarget struct {
	Name        string  // human-readable service name
	Kind        string  // rtty, cw, navtex, wefax, ft8, wspr
	CarrierKHz  float64 // published carrier/centre frequency
	DialKHz     float64 // receiver dial frequency (sideband offset applied)
	Mode        string  // receiver demodulation mode (usb, lsb, am, cw, cwn)
	Baud        float64 // symbol rate for RTTY/NAVTEX
	MarkHz      float64 // expected mark tone in the audio passband
	SpaceHz     float64 // expected space tone in the audio passband
	Inverted    bool    // mark is the lower RF tone (DWD F1B convention)
	LinesPerMin float64 // fax drum speed
	IOC         int     // fax index of cooperation
	Expect      string  // quality-gate anchor: callsign or phrase a real decode must contain
}

// HFBroadcasts lists the services monitored for live HF decoding.
// Frequencies are carrier centres; DialKHz places the wanted tones inside
// the receiver's USB passband (300-2700 Hz).
//
// DWD RTTY (Deutscher Wetterdienst, Pinneberg): 50 baud, 450 Hz shift,
// F1B, mark LOW. With a USB dial 1.9 kHz below the centre the mark lands
// at 1675 Hz and space at 2125 Hz. Verified live 2026-10-21 on 4583 kHz
// ("CQ CQ CQ DE DDK2 DDH7 DDK9").
var HFBroadcasts = []HFTarget{
	{
		Name: "DWD RTTY 4583 (night)", Kind: "rtty",
		CarrierKHz: 4583.0, DialKHz: 4581.1, Mode: "usb",
		Baud: 50, MarkHz: 1675, SpaceHz: 2125, Inverted: true,
		Expect: "DDK2",
	},
	{
		Name: "DWD RTTY 7646", Kind: "rtty",
		CarrierKHz: 7646.0, DialKHz: 7644.1, Mode: "usb",
		Baud: 50, MarkHz: 1675, SpaceHz: 2125, Inverted: true,
		Expect: "DDH7",
	},
	{
		Name: "DWD RTTY 10100.8 (day)", Kind: "rtty",
		CarrierKHz: 10100.8, DialKHz: 10098.9, Mode: "usb",
		Baud: 50, MarkHz: 1675, SpaceHz: 2125, Inverted: true,
		Expect: "DDK9",
	},
	{
		Name: "OK0EU CW beacon", Kind: "cw",
		CarrierKHz: 7038.5, DialKHz: 7037.8, Mode: "usb",
		Expect: "OK0EU",
	},
	{
		Name: "NAVTEX 518", Kind: "navtex",
		CarrierKHz: 518.0, DialKHz: 516.3, Mode: "usb",
		Baud: 100, MarkHz: 1785, SpaceHz: 1615,
	},
	{
		Name: "DWD WEFAX 3855 (night)", Kind: "wefax",
		CarrierKHz: 3855.0, DialKHz: 3853.1, Mode: "usb",
		LinesPerMin: 120, IOC: 576,
	},
	{
		Name: "DWD WEFAX 7880", Kind: "wefax",
		CarrierKHz: 7880.0, DialKHz: 7878.1, Mode: "usb",
		LinesPerMin: 120, IOC: 576,
	},
	{
		Name: "DWD WEFAX 13882.5 (day)", Kind: "wefax",
		CarrierKHz: 13882.5, DialKHz: 13880.6, Mode: "usb",
		LinesPerMin: 120, IOC: 576,
	},
	{
		Name: "RWM time signal CW 4996", Kind: "cw",
		CarrierKHz: 4996.0, DialKHz: 4995.3, Mode: "usb",
		Expect: "RWM",
	},
	{
		Name: "RWM time signal CW 9996", Kind: "cw",
		CarrierKHz: 9996.0, DialKHz: 9995.3, Mode: "usb",
		Expect: "RWM",
	},
	{
		Name: "RWM time signal CW 14996", Kind: "cw",
		CarrierKHz: 14996.0, DialKHz: 14995.3, Mode: "usb",
		Expect: "RWM",
	},
	{
		Name: "PSK31 40m", Kind: "psk31",
		CarrierKHz: 7070.0, DialKHz: 7070.0, Mode: "usb",
	},
	{
		Name: "FT4 40m", Kind: "ft8",
		CarrierKHz: 7047.5, DialKHz: 7047.5, Mode: "usb",
	},
	{
		Name: "FT8 40m", Kind: "ft8",
		CarrierKHz: 7074.0, DialKHz: 7074.0, Mode: "usb",
	},
	{
		Name: "FT8 80m (late)", Kind: "ft8",
		CarrierKHz: 3573.0, DialKHz: 3573.0, Mode: "usb",
	},
	{
		Name: "WSPR 40m", Kind: "wspr",
		CarrierKHz: 7038.6, DialKHz: 7038.6, Mode: "usb",
	},
	{
		Name: "DSC distress 8414.5", Kind: "dsc",
		CarrierKHz: 8414.5, DialKHz: 8414.5, Mode: "usb",
	},
	{
		Name: "HFDL 5720", Kind: "hfdl",
		CarrierKHz: 5720.0, DialKHz: 5720.0, Mode: "usb",
	},
	{
		Name: "HFDL 6628", Kind: "hfdl",
		CarrierKHz: 6628.0, DialKHz: 6628.0, Mode: "usb",
	},
	{
		Name: "HFDL 8927", Kind: "hfdl",
		CarrierKHz: 8927.0, DialKHz: 8927.0, Mode: "usb",
	},
	{
		Name: "NCDXF beacon 14100 (day)", Kind: "cw",
		CarrierKHz: 14100.0, DialKHz: 14099.3, Mode: "usb",
	},
	// --- Terrestrial widening (user steer 2026-10-22): LF/MF/HF utility,
	// time signals, marine, aero. Kinds without a built-in decoder
	// (dsc, hfdl, ft8, wspr, voice) capture audio only until a decoder
	// lands; nothing reaches the gallery without the verify gate.
	{
		Name: "DDH47 RTTY 147.3 LF", Kind: "rtty",
		CarrierKHz: 147.3, DialKHz: 145.4, Mode: "usb",
		Baud: 50, MarkHz: 1675, SpaceHz: 2125, Inverted: true,
		Expect: "DDH47",
	},
	{
		Name: "NAVTEX 490 (national)", Kind: "navtex",
		CarrierKHz: 490.0, DialKHz: 488.3, Mode: "usb",
		Baud: 100, MarkHz: 1785, SpaceHz: 1615,
	},
	{
		Name: "NAVTEX 4209.5", Kind: "navtex",
		CarrierKHz: 4209.5, DialKHz: 4207.8, Mode: "usb",
		Baud: 100, MarkHz: 1785, SpaceHz: 1615,
	},
	{
		Name: "BPM time signal 5000", Kind: "cw",
		CarrierKHz: 5000.0, DialKHz: 4999.3, Mode: "usb",
		Expect: "BPM",
	},
	{
		Name: "BPM time signal 10000", Kind: "cw",
		CarrierKHz: 10000.0, DialKHz: 9999.3, Mode: "usb",
		Expect: "BPM",
	},
	{
		Name: "WWV voice 10000", Kind: "voice",
		CarrierKHz: 10000.0, DialKHz: 10000.0, Mode: "am",
	},
	{
		Name: "WWV voice 15000", Kind: "voice",
		CarrierKHz: 15000.0, DialKHz: 15000.0, Mode: "am",
	},
	{
		Name: "CHU time 7850", Kind: "voice",
		CarrierKHz: 7850.0, DialKHz: 7850.0, Mode: "usb",
	},
	{
		Name: "DWD Seewetter voice 5905", Kind: "voice",
		CarrierKHz: 5905.0, DialKHz: 5905.0, Mode: "am",
	},
	{
		Name: "DWD Seewetter voice 6180", Kind: "voice",
		CarrierKHz: 6180.0, DialKHz: 6180.0, Mode: "am",
	},
	{
		Name: "NCDXF beacon 18110", Kind: "cw",
		CarrierKHz: 18110.0, DialKHz: 18109.3, Mode: "usb",
	},
	{
		Name: "NCDXF beacon 21150", Kind: "cw",
		CarrierKHz: 21150.0, DialKHz: 21149.3, Mode: "usb",
	},
	{
		Name: "NCDXF beacon 24930", Kind: "cw",
		CarrierKHz: 24930.0, DialKHz: 24929.3, Mode: "usb",
	},
	{
		Name: "NCDXF beacon 28200", Kind: "cw",
		CarrierKHz: 28200.0, DialKHz: 28199.3, Mode: "usb",
	},
	{
		Name: "DSC distress 2187.5", Kind: "dsc",
		CarrierKHz: 2187.5, DialKHz: 2187.5, Mode: "usb",
	},
	{
		Name: "DSC distress 6312", Kind: "dsc",
		CarrierKHz: 6312.0, DialKHz: 6312.0, Mode: "usb",
	},
	{
		Name: "DSC distress 12577", Kind: "dsc",
		CarrierKHz: 12577.0, DialKHz: 12577.0, Mode: "usb",
	},
	{
		Name: "DSC distress 16804.5", Kind: "dsc",
		CarrierKHz: 16804.5, DialKHz: 16804.5, Mode: "usb",
	},
	{
		Name: "HFDL 10084 (day)", Kind: "hfdl",
		CarrierKHz: 10084.0, DialKHz: 10084.0, Mode: "usb",
	},
	{
		Name: "HFDL 11384 (day)", Kind: "hfdl",
		CarrierKHz: 11384.0, DialKHz: 11384.0, Mode: "usb",
	},
	{
		Name: "PSK31 20m", Kind: "psk31",
		CarrierKHz: 14070.0, DialKHz: 14070.0, Mode: "usb",
	},
	{
		Name: "FT8 20m", Kind: "ft8",
		CarrierKHz: 14074.0, DialKHz: 14074.0, Mode: "usb",
	},
	{
		Name: "WSPR 20m", Kind: "wspr",
		CarrierKHz: 14095.6, DialKHz: 14095.6, Mode: "usb",
	},
}

// ActiveNow reports whether the target plausibly transmits at t, given its
// rough schedule. 24/7 services always qualify; DWD fax runs fixed slots.
func (t HFTarget) ActiveNow(now time.Time) bool {
	utc := now.UTC()
	switch t.Kind {
	case "rtty", "cw", "navtex":
		return true // 24/7 services (NAVTEX in 10-minute slots every 4 h)
	case "wefax":
		// DWD broadcast windows (UTC) per the BAmD schedule.
		m := utc.Hour()*60 + utc.Minute()
		windows := [][2]int{{6*60 + 0, 19*60 + 39}, {21*60 + 0, 23*60 + 59}}
		for _, w := range windows {
			if m >= w[0] && m <= w[1] {
				return true
			}
		}
		return false
	}
	return true
}
