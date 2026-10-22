package decode

import (
	"regexp"
	"sort"
	"strings"
)

// SAME (Specific Area Message Encoding) as broadcast by NOAA Weather Radio.
//
// EASDecode reads one burst of AFSK. A real alert sends the header three
// times a second apart (47 CFR 11.31), possibly inside minutes of other
// audio, so SAMEScan cuts a long recording into overlapping windows, decodes
// each, repairs bit errors by voting across the repeats, and keeps only
// headers that satisfy the FCC field layout. That filter matters on speech
// and music: the demodulator will otherwise print noise as characters.
//
// Format: ZCZC-ORG-EEE-PSSCCC[-PSSCCC...]+TTTT-JJJHHMM-LLLLLLLL-
// https://www.ecfr.gov/current/title-47/chapter-I/subchapter-A/part-11/subpart-B/section-11.31

// SAMEAlert is a validated, decoded SAME header.
type SAMEAlert struct {
	EASAlert
	Repeats     int      // how many bursts agreed after voting
	EventName   string   // e.g. "Tornado Warning"
	OriginName  string   // e.g. "National Weather Service"
	Areas       []string // human readable: "Cuyahoga County, OH (039035)"
	Issued      string   // JJJHHMM as "day 142 18:00 UTC"
	DurationMin int      // minutes
}

var sameFindRE = regexp.MustCompile(`ZCZC-[A-Z]{3}-[A-Z0-9]{3}(?:-\d{6}){1,31}\+\d{4}-\d{7}-[A-Z0-9/ ]{1,8}-`)

var sameHeaderRE = regexp.MustCompile(`^ZCZC-([A-Z]{3})-([A-Z0-9]{3})((?:-\d{6}){1,31})\+(\d{4})-(\d{7})-([A-Z0-9/ ]{1,8})-$`)

// ValidSAME reports whether raw is a complete, well formed SAME header.
func ValidSAME(raw string) bool { return sameHeaderRE.MatchString(raw) }

// SAMEScan finds SAME headers anywhere in samples.
func SAMEScan(samples []float64, rate int) []SAMEAlert {
	if rate < 8000 {
		return nil
	}
	var cands []string
	for _, b := range sameBursts(samples, rate) {
		for _, a := range EASDecode(samples[b[0]:b[1]], rate) {
			if a.End {
				continue
			}
			// The demodulator keeps reading printable bytes after the
			// closing dash; cut the header at its real end.
			if m := sameFindRE.FindString(a.Raw); m != "" {
				cands = append(cands, m)
			} else {
				cands = append(cands, a.Raw)
			}
		}
	}
	return VoteSAME(cands)
}

// sameBursts returns [start,end) sample ranges where almost all of the
// energy sits on the SAME mark and space tones. Decoding each burst on its
// own gives every repeat its own bit clock; one clock across several
// bursts drifts by the gap between them.
func sameBursts(samples []float64, rate int) [][2]int {
	frame := rate / 50 // 20 ms
	if frame < 64 || len(samples) < 3*frame {
		return nil
	}
	n := len(samples) / frame
	tone := make([]bool, n)
	for i := 0; i < n; i++ {
		seg := samples[i*frame : (i+1)*frame]
		var e float64
		for _, v := range seg {
			e += v * v
		}
		if e < 1e-6*float64(frame) {
			continue
		}
		// Goertzel power is |X|^2; a pure tone of amplitude A has
		// |X|^2 = (A*N/2)^2 and energy A^2*N/2.
		p := goertzel(seg, rate, easMarkHz) + goertzel(seg, rate, easSpaceHz)
		tone[i] = p*2/float64(frame)/e > 0.5
	}
	var out [][2]int
	gap := 8 // 160 ms of non-tone frames ends a burst
	for i := 0; i < n; {
		if !tone[i] {
			i++
			continue
		}
		start, last, miss := i, i, 0
		for j := i; j < n && miss <= gap; j++ {
			if tone[j] {
				last, miss = j, 0
			} else {
				miss++
			}
		}
		i = last + 1
		if float64(last-start+1)*0.02 < 0.8 {
			continue
		}
		pad := frame * 5
		s, e := start*frame-pad, (last+1)*frame+pad
		if s < 0 {
			s = 0
		}
		if e > len(samples) {
			e = len(samples)
		}
		out = append(out, [2]int{s, e})
	}
	return out
}

// VoteSAME groups candidate header strings and repairs them. Strings that
// already validate are grouped by identity. Invalid ones of equal length are
// merged by per-character majority, which is how receivers use the three
// repeats; the merge is accepted only if it then validates.
func VoteSAME(cands []string) []SAMEAlert {
	byLen := map[int][]string{}
	for _, c := range cands {
		if strings.HasPrefix(c, "ZCZC-") {
			byLen[len(c)] = append(byLen[len(c)], c)
		}
	}
	count := map[string]int{}
	for _, group := range byLen {
		distinct := map[string]int{}
		for _, g := range group {
			distinct[g]++
		}
		if len(distinct) == 1 {
			if ValidSAME(group[0]) {
				count[group[0]] += len(group)
			}
			continue
		}
		if len(group) >= 3 {
			merged := make([]byte, len(group[0]))
			for i := range merged {
				votes := map[byte]int{}
				for _, g := range group {
					votes[g[i]]++
				}
				best, bn := byte(0), 0
				for b, n := range votes {
					if n > bn || (n == bn && b < best) {
						best, bn = b, n
					}
				}
				merged[i] = best
			}
			if ValidSAME(string(merged)) {
				count[string(merged)] += len(group)
				continue
			}
		}
		// No majority repair possible: keep only exact, well formed reads.
		for g, n := range distinct {
			if ValidSAME(g) {
				count[g] += n
			}
		}
	}
	var raws []string
	for r, n := range count {
		// A read that differs from a better supported one by a few
		// characters is that read with bit errors, not a second alert.
		dominated := false
		for o, m := range count {
			if o != r && len(o) == len(r) && m > n && sameDistance(o, r) <= 4 {
				dominated = true
			}
		}
		if !dominated {
			raws = append(raws, r)
		}
	}
	sort.Strings(raws)
	var out []SAMEAlert
	for _, r := range raws {
		out = append(out, describeSAME(r, count[r]))
	}
	return out
}

func describeSAME(raw string, repeats int) SAMEAlert {
	e := parseEASHeader(raw)
	a := SAMEAlert{EASAlert: e, Repeats: repeats}
	a.EventName = sameEvents[e.Event]
	if a.EventName == "" {
		a.EventName = "unknown event " + e.Event
	}
	a.OriginName = sameOrigins[e.Origin]
	for _, loc := range e.Locations {
		a.Areas = append(a.Areas, describeFIPS(loc))
	}
	if len(e.Duration) == 4 {
		a.DurationMin = atoi2(e.Duration[:2])*60 + atoi2(e.Duration[2:])
	}
	if len(e.Timestamp) == 7 {
		a.Issued = "day " + e.Timestamp[:3] + " " + e.Timestamp[3:5] + ":" + e.Timestamp[5:] + " UTC"
	}
	return a
}

func atoi2(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}

// describeFIPS renders PSSCCC (portion, state FIPS, county FIPS).
func describeFIPS(code string) string {
	if len(code) != 6 {
		return code
	}
	st := sameStates[code[1:3]]
	county := code[3:]
	var s string
	switch {
	case county == "000":
		s = "entire state"
	default:
		s = "county " + county
	}
	if st != "" {
		s += ", " + st
	}
	if p := code[:1]; p != "0" {
		s = "portion " + p + " of " + s
	}
	return s + " (" + code + ")"
}

// String is a one-line summary.
func (a SAMEAlert) String() string {
	return a.EventName + " [" + a.Event + "] from " + a.Origin + " for " + strings.Join(a.Areas, "; ") +
		", issued " + a.Issued + ", lasts " + itoa(a.DurationMin) + " min, sender " + a.Station +
		" (" + itoa(a.Repeats) + " repeat(s) agreed)"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

// Event codes from 47 CFR 11.31(e) plus the NWS additions in current use.
var sameEvents = map[string]string{
	"EAN": "Emergency Action Notification", "EAT": "Emergency Action Termination", "NIC": "National Information Center",
	"NPT": "National Periodic Test", "RMT": "Required Monthly Test", "RWT": "Required Weekly Test",
	"ADR": "Administrative Message", "AVA": "Avalanche Watch", "AVW": "Avalanche Warning",
	"BZW": "Blizzard Warning", "CAE": "Child Abduction Emergency", "CDW": "Civil Danger Warning",
	"CEM": "Civil Emergency Message", "CFA": "Coastal Flood Watch", "CFW": "Coastal Flood Warning",
	"DSW": "Dust Storm Warning", "EQW": "Earthquake Warning", "EVI": "Evacuation Immediate",
	"EWW": "Extreme Wind Warning", "FFA": "Flash Flood Watch", "FFS": "Flash Flood Statement",
	"FFW": "Flash Flood Warning", "FLA": "Flood Watch", "FLS": "Flood Statement", "FLW": "Flood Warning",
	"FRW": "Fire Warning", "FSW": "Flash Freeze Warning", "FZW": "Freeze Warning",
	"HLS": "Hurricane Local Statement", "HMW": "Hazardous Materials Warning", "HUA": "Hurricane Watch",
	"HUW": "Hurricane Warning", "HWA": "High Wind Watch", "HWW": "High Wind Warning",
	"LAE": "Local Area Emergency", "LEW": "Law Enforcement Warning", "NMN": "Network Message Notification",
	"NUW": "Nuclear Power Plant Warning", "RHW": "Radiological Hazard Warning", "SMW": "Special Marine Warning",
	"SPS": "Special Weather Statement", "SPW": "Shelter In Place Warning", "SQW": "Snow Squall Warning",
	"SSA": "Storm Surge Watch", "SSW": "Storm Surge Warning", "SVA": "Severe Thunderstorm Watch",
	"SVR": "Severe Thunderstorm Warning", "SVS": "Severe Weather Statement", "TOA": "Tornado Watch",
	"TOE": "911 Telephone Outage Emergency", "TOR": "Tornado Warning", "TRA": "Tropical Storm Watch",
	"TRW": "Tropical Storm Warning", "TSA": "Tsunami Watch", "TSW": "Tsunami Warning",
	"VOW": "Volcano Warning", "WFA": "Wildfire Watch", "WFW": "Wildfire Warning",
	"WSA": "Winter Storm Watch", "WSW": "Winter Storm Warning",
}

var sameOrigins = map[string]string{
	"EAS": "Broadcast station or cable system", "CIV": "Civil authorities",
	"PEP": "Primary Entry Point system", "WXR": "National Weather Service",
}

// US state FIPS codes (SAME uses the same two digit numbers).
var sameStates = map[string]string{
	"01": "AL", "02": "AK", "04": "AZ", "05": "AR", "06": "CA", "08": "CO", "09": "CT", "10": "DE", "11": "DC",
	"12": "FL", "13": "GA", "15": "HI", "16": "ID", "17": "IL", "18": "IN", "19": "IA", "20": "KS", "21": "KY",
	"22": "LA", "23": "ME", "24": "MD", "25": "MA", "26": "MI", "27": "MN", "28": "MS", "29": "MO", "30": "MT",
	"31": "NE", "32": "NV", "33": "NH", "34": "NJ", "35": "NM", "36": "NY", "37": "NC", "38": "ND", "39": "OH",
	"40": "OK", "41": "OR", "42": "PA", "44": "RI", "45": "SC", "46": "SD", "47": "TN", "48": "TX", "49": "UT",
	"50": "VT", "51": "VA", "53": "WA", "54": "WV", "55": "WI", "56": "WY", "60": "AS", "66": "GU", "69": "MP",
	"72": "PR", "78": "VI", "57": "PZ (Pacific coastal)", "58": "PK (Alaska coastal)", "59": "PH (Hawaii coastal)",
	"61": "PM (Marianas coastal)", "65": "PS (American Samoa coastal)", "73": "GM (Gulf of Mexico)",
	"75": "ST (Atlantic coastal)", "77": "LS (Lake Superior)",
}

func sameDistance(a, b string) int {
	d := 0
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			d++
		}
	}
	return d
}
