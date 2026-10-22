package decode

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

// APRSInfo is the parsed payload of an APRS packet (APRS101 chapters 5-10).
// Fields that a packet does not carry stay at their zero value; HasPos says
// whether Lat/Lon are valid. Speed is in km/h, Altitude in metres.
type APRSInfo struct {
	Kind     string // position, mic-e, message, status, object, item, weather, telemetry, other
	HasPos   bool
	Lat, Lon float64
	Symbol   string // table char + symbol char
	Course   int
	Speed    float64
	Altitude float64
	HasAlt   bool
	Comment  string
	Name     string // object or item name
	To       string // message addressee
	Text     string // message or status text
	MsgID    string
	Weather  map[string]float64
	MicEMsg  string
}

var (
	aprsPlainPos = regexp.MustCompile(`^(\d{2})([\d ]{2}\.[\d ]{2})([NS])(.)(\d{3})([\d ]{2}\.[\d ]{2})([EW])(.)(.*)$`)
	aprsAltRe    = regexp.MustCompile(`/A=(-?\d{6})`)
	aprsCSERe    = regexp.MustCompile(`^(\d{3})/(\d{3})`)
	aprsWxRe     = regexp.MustCompile(`([cstrpPhb])(\d+|\.+)`)
)

func aprsDM(deg, min string) float64 {
	d, _ := strconv.Atoi(deg)
	min = strings.ReplaceAll(min, " ", "0")
	m, _ := strconv.ParseFloat(min, 64)
	return float64(d) + m/60
}

// ParseAPRS parses an AX.25 UI payload. dest is the destination callsign
// (needed for Mic-E, which hides the latitude in it).
func ParseAPRS(dest, info string) APRSInfo {
	if info == "" {
		return APRSInfo{Kind: "other"}
	}
	t := info[0]
	switch t {
	case '`', '\'':
		if r, ok := aprsMicE(dest, info); ok {
			return r
		}
	case '!', '=':
		return aprsPosition(info[1:], t)
	case '/', '@':
		if len(info) >= 8 {
			return aprsPosition(info[8:], t)
		}
	case ':':
		if len(info) >= 11 && info[10] == ':' {
			r := APRSInfo{Kind: "message", To: strings.TrimSpace(info[1:10]), Text: strings.TrimSpace(info[11:])}
			if i := strings.LastIndex(r.Text, "{"); i >= 0 {
				r.MsgID = r.Text[i+1:]
				r.Text = r.Text[:i]
			}
			return r
		}
	case '>':
		return APRSInfo{Kind: "status", Text: info[1:]}
	case ';':
		if len(info) >= 18 {
			r := aprsPosition(info[18:], ';')
			r.Kind = "object"
			r.Name = strings.TrimSpace(info[1:10])
			return r
		}
	case ')':
		if i := strings.IndexAny(info[1:], "!_"); i >= 0 {
			r := aprsPosition(info[i+2:], ')')
			r.Kind = "item"
			r.Name = info[1 : i+1]
			return r
		}
	case '_':
		r := APRSInfo{Kind: "weather"}
		if len(info) > 9 {
			r.Weather = aprsWeather(info[9:])
		}
		return r
	case '$':
		if r, ok := aprsNMEA(info); ok {
			return r
		}
	case 'T':
		if strings.HasPrefix(info, "T#") {
			return APRSInfo{Kind: "telemetry", Text: info[2:]}
		}
	}
	return APRSInfo{Kind: "other", Text: info}
}

func aprsPosition(s string, t byte) APRSInfo {
	r := APRSInfo{Kind: "position"}
	if m := aprsPlainPos.FindStringSubmatch(s); m != nil {
		r.Lat = aprsDM(m[1], m[2])
		if m[3] == "S" {
			r.Lat = -r.Lat
		}
		r.Lon = aprsDM(m[5], m[6])
		if m[7] == "W" {
			r.Lon = -r.Lon
		}
		r.HasPos = true
		r.Symbol = m[4] + m[8]
		r.Comment = aprsExtras(&r, m[9])
		return r
	}
	// Compressed: table, 4 chars lat, 4 chars lon, symbol, 2 cs chars, type.
	if len(s) >= 13 && (strings.ContainsRune("/\\", rune(s[0])) || (s[0] >= 'A' && s[0] <= 'j')) {
		b91 := func(x string) (float64, bool) {
			v := 0.0
			for i := 0; i < len(x); i++ {
				if x[i] < 33 || x[i] > 124 {
					return 0, false
				}
				v = v*91 + float64(x[i]-33)
			}
			return v, true
		}
		la, ok1 := b91(s[1:5])
		lo, ok2 := b91(s[5:9])
		if ok1 && ok2 {
			r.Lat = 90 - la/380926
			r.Lon = -180 + lo/190463
			r.HasPos = true
			r.Symbol = string(s[0]) + string(s[9])
			c, sp, ty := s[10], s[11], s[12]
			if (ty-33)&0x18 == 0x10 && c != ' ' { // GGA altitude
				cs := float64(int(c-33)*91 + int(sp-33))
				r.Altitude = math.Pow(1.002, cs) * 0.3048
				r.HasAlt = true
			} else if c >= 33 && c <= 122 {
				r.Course = int(c-33) * 4
				r.Speed = (math.Pow(1.08, float64(sp-33)) - 1) * 1.852
			}
			r.Comment = strings.TrimSpace(aprsExtras(&r, s[13:]))
			return r
		}
	}
	r.Kind = "other"
	r.Text = s
	return r
}

// aprsExtras pulls course/speed, altitude and weather out of a comment and
// returns the remaining text.
func aprsExtras(r *APRSInfo, c string) string {
	if m := aprsCSERe.FindStringSubmatch(c); m != nil && !strings.HasPrefix(r.Symbol[1:], "_") {
		r.Course, _ = strconv.Atoi(m[1])
		kn, _ := strconv.Atoi(m[2])
		r.Speed = float64(kn) * 1.852
		c = c[7:]
	}
	if m := aprsAltRe.FindStringSubmatch(c); m != nil {
		ft, _ := strconv.Atoi(m[1])
		r.Altitude = float64(ft) * 0.3048
		r.HasAlt = true
		c = strings.Replace(c, m[0], "", 1)
	}
	if len(r.Symbol) == 2 && r.Symbol[1] == '_' {
		r.Kind = "weather"
		if m := aprsCSERe.FindStringSubmatch(c); m != nil {
			r.Weather = aprsWeather(c)
			r.Weather["wind_dir"], _ = strconv.ParseFloat(m[1], 64)
			r.Weather["wind_speed_mph"], _ = strconv.ParseFloat(m[2], 64)
		} else {
			r.Weather = aprsWeather(c)
		}
	}
	return c
}

func aprsWeather(s string) map[string]float64 {
	w := map[string]float64{}
	if m := regexp.MustCompile(`c(\d{3})s(\d{3})`).FindStringSubmatch(s); m != nil {
		w["wind_dir"], _ = strconv.ParseFloat(m[1], 64)
		w["wind_speed_mph"], _ = strconv.ParseFloat(m[2], 64)
	}
	names := map[string]string{"g": "gust_mph", "t": "temp_f", "r": "rain_1h_hundredths_in", "p": "rain_24h_hundredths_in", "P": "rain_midnight_hundredths_in", "h": "humidity_pct", "b": "pressure_tenths_hpa"}
	for _, m := range regexp.MustCompile(`([gtrpPhb])(-?\d{2,5})`).FindAllStringSubmatch(s, -1) {
		v, _ := strconv.ParseFloat(m[2], 64)
		w[names[m[1]]] = v
	}
	return w
}

// aprsMicE decodes a Mic-E packet (APRS101 chapter 10).
func aprsMicE(dest, info string) (APRSInfo, bool) {
	r := APRSInfo{Kind: "mic-e"}
	if i := strings.IndexByte(dest, '-'); i >= 0 {
		dest = dest[:i]
	}
	if len(dest) < 6 || len(info) < 9 {
		return r, false
	}
	var digits [6]int
	amb := 0
	var cls [6]int // 0 = digit/L (flag 0), 1 = P-Y (flag 1 std), 2 = A-K custom
	for i := 0; i < 6; i++ {
		c := dest[i]
		switch {
		case c >= '0' && c <= '9':
			digits[i] = int(c - '0')
		case c >= 'A' && c <= 'J':
			digits[i], cls[i] = int(c-'A'), 2
		case c >= 'P' && c <= 'Y':
			digits[i], cls[i] = int(c-'P'), 1
		case c == 'K' || c == 'L' || c == 'Z': // space: position ambiguity
			digits[i] = 0
			amb++
			if c == 'K' {
				cls[i] = 2
			} else if c == 'Z' {
				cls[i] = 1
			}
		default:
			return r, false
		}
	}
	// Ambiguous (space) digits take the middle of the masked range.
	latMin := digits[2]*10 + digits[3]
	latHund := digits[4]*10 + digits[5]
	switch {
	case amb == 1:
		latHund = digits[4]*10 + 5
	case amb == 2:
		latHund = 50
	case amb == 3:
		latMin, latHund = digits[2]*10+5, 50
	case amb >= 4:
		latMin, latHund = 30, 0
	}
	lat := float64(digits[0]*10+digits[1]) + float64(latMin)/60 + float64(latHund)/6000
	north := cls[3] != 0
	lonOff := cls[4] != 0
	west := cls[5] != 0
	if !north {
		lat = -lat
	}
	d := int(info[1]) - 28
	if lonOff {
		d += 100
	}
	if d >= 190 {
		d -= 190
	} else if d >= 180 {
		d -= 80
	}
	m := int(info[2]) - 28
	if m >= 60 {
		m -= 60
	}
	h := int(info[3]) - 28
	switch {
	case amb == 1:
		h = h/10*10 + 5
	case amb == 2:
		h = 50
	case amb == 3:
		m, h = m/10*10+5, 50
	case amb >= 4:
		m, h = 30, 0
	}
	lon := float64(d) + float64(m)/60 + float64(h)/6000
	if west {
		lon = -lon
	}
	sp := (int(info[4])-28)*10 + (int(info[5])-28)/10
	cse := ((int(info[5])-28)%10)*100 + (int(info[6]) - 28)
	if sp >= 800 {
		sp -= 800
	}
	if cse >= 400 {
		cse -= 400
	}
	if tb := info[8]; !(tb == '/' || tb == '\\' || (tb >= 'A' && tb <= 'Z') || (tb >= '0' && tb <= '9') || (tb >= 'a' && tb <= 'j')) {
		return r, false
	}
	r.Lat, r.Lon, r.HasPos = lat, lon, true
	r.Speed = float64(sp) * 1.852
	r.Course = cse
	r.Symbol = string(info[8]) + string(info[7])
	rest := info[9:]
	// Optional altitude: three base-91 chars followed by '}', metres above -10000.
	if i := strings.IndexByte(rest, '}'); i >= 3 {
		a := 0
		for _, c := range []byte(rest[i-3 : i]) {
			a = a*91 + int(c) - 33
		}
		r.Altitude = float64(a - 10000)
		r.HasAlt = true
		rest = rest[:i-3] + rest[i+1:]
	}
	r.Comment = strings.TrimSpace(rest)
	return r, true
}

func nmeaCoord(v, hemi string, degDigits int) (float64, bool) {
	if len(v) < degDigits+2 || hemi == "" {
		return 0, false
	}
	d, err1 := strconv.Atoi(v[:degDigits])
	m, err2 := strconv.ParseFloat(v[degDigits:], 64)
	if err1 != nil || err2 != nil {
		return 0, false
	}
	x := float64(d) + m/60
	if hemi == "S" || hemi == "W" {
		x = -x
	}
	return x, true
}

// aprsNMEA handles raw NMEA sentences some trackers send (GPRMC, GPGGA, GPGLL).
func aprsNMEA(s string) (APRSInfo, bool) {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '*'); i >= 0 {
		s = s[:i]
	}
	f := strings.Split(s, ",")
	r := APRSInfo{Kind: "nmea"}
	var latS, nsS, lonS, ewS string
	switch f[0] {
	case "$GPRMC", "$GNRMC":
		if len(f) < 9 || f[2] != "A" {
			return r, false
		}
		latS, nsS, lonS, ewS = f[3], f[4], f[5], f[6]
		kn, _ := strconv.ParseFloat(f[7], 64)
		r.Speed = kn * 1.852
		c, _ := strconv.ParseFloat(f[8], 64)
		r.Course = int(c + 0.5)
	case "$GPGGA", "$GNGGA":
		if len(f) < 10 || f[6] == "0" || f[6] == "" {
			return r, false
		}
		latS, nsS, lonS, ewS = f[2], f[3], f[4], f[5]
		if a, err := strconv.ParseFloat(f[9], 64); err == nil {
			r.Altitude, r.HasAlt = a, true
		}
	case "$GPGLL", "$GNGLL":
		if len(f) < 6 {
			return r, false
		}
		latS, nsS, lonS, ewS = f[1], f[2], f[3], f[4]
	default:
		return r, false
	}
	la, ok1 := nmeaCoord(latS, nsS, 2)
	lo, ok2 := nmeaCoord(lonS, ewS, 3)
	if !ok1 || !ok2 {
		return r, false
	}
	r.Lat, r.Lon, r.HasPos = la, lo, true
	return r, true
}
