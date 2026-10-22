package decode

// Mode S / ADS-B downlink frame decoding (ICAO Annex 10 / RTCA DO-260).
//
// Frames arrive as hex strings of 14 nibbles (56-bit short squitter) or
// 28 nibbles (112-bit long squitter) from community feeds and decoders.
// The decoder here is original code: it validates the CRC-24 checksum with
// the Mode S generator polynomial, splits the downlink formats and decodes
// the DF17/DF18 extended squitter ME field: aircraft identification,
// airborne position (compact position reporting), velocity, surface
// position and status. CPR global decode needs one even and one odd frame
// from the same aircraft.

import (
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"time"
)

// ModeSMessage is one decoded Mode S frame.
type ModeSMessage struct {
	DF       int    `json:"df"`
	ICAO24   string `json:"icao24"`
	TC       int    `json:"tc,omitempty"` // DF17/18 type code
	Callsign string `json:"callsign,omitempty"`
	Altitude int    `json:"altitude_ft,omitempty"`
	HasAlt   bool   `json:"has_alt,omitempty"`
	Raw      string `json:"raw"` // the hex frame
	// CPR position components (airborne or surface).
	CPROdd bool    `json:"cpr_odd,omitempty"`
	CPRLat float64 `json:"cpr_lat,omitempty"` // 0..1
	CPRLon float64 `json:"cpr_lon,omitempty"`
	HasCPR bool    `json:"has_cpr,omitempty"`
	// Velocity (TC 19).
	SpeedKt  float64 `json:"speed_kt,omitempty"`
	TrackDeg float64 `json:"track_deg,omitempty"`
	VertRate int     `json:"vert_rate_fpm,omitempty"`
	HasVel   bool    `json:"has_vel,omitempty"`
	// Surface (TC 5-8).
	GroundSpeedKt float64   `json:"ground_speed_kt,omitempty"`
	SurfaceTrack  float64   `json:"surface_track_deg,omitempty"`
	HasSurface    bool      `json:"has_surface,omitempty"`
	Summary       string    `json:"summary,omitempty"`
	When          time.Time `json:"when,omitempty"` // observation time, set by the caller
}

// modesPoly is the Mode S CRC generator without its top bit:
// G(x) = x^24 + x^23 + ... + 1 -> 0x1FFF409.
const modesPoly = 0xFFF409

// modesSyndrome feeds the whole frame through the CRC LFSR. A frame whose
// parity is a plain CRC (DF17/18 extended squitter) yields zero; a frame
// with address-parity overlay (DF0/4/5/11/16/20/21) yields the ICAO24
// address.
func modesSyndrome(msg []byte, bits int) uint32 {
	var rem uint32
	for i := 0; i < bits; i++ {
		bit := uint32(msg[i/8]>>(7-uint(i%8))) & 1
		top := rem >> 23
		rem = ((rem << 1) | bit) & 0xFFFFFF
		if top == 1 {
			rem ^= modesPoly
		}
	}
	return rem & 0xFFFFFF
}

func getbits(msg []byte, start, n int) uint64 {
	var v uint64
	for i := 0; i < n; i++ {
		p := start + i
		v = v<<1 | uint64(msg[p/8]>>(7-uint(p%8)))&1
	}
	return v
}

// aisCharset is the 6-bit ICAO flight-ID alphabet: 1..26 are A..Z, 32 is
// space, 48..57 are 0..9.
func flightChar(v uint64) byte {
	switch {
	case v >= 1 && v <= 26:
		return byte('A' + v - 1)
	case v == 32:
		return ' '
	case v >= 48 && v <= 57:
		return byte('0' + v - 48)
	default:
		return 0
	}
}

// DecodeModeS decodes one hex Mode S frame (7 or 14 bytes).
func DecodeModeS(frame string) (*ModeSMessage, error) {
	f := strings.ToUpper(strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(frame, "*"), ";")))
	msg, err := hex.DecodeString(f)
	if err != nil {
		return nil, fmt.Errorf("bad hex frame: %w", err)
	}
	if len(msg) != 7 && len(msg) != 14 {
		return nil, fmt.Errorf("bad frame length %d bytes", len(msg))
	}
	bits := len(msg) * 8
	m := &ModeSMessage{Raw: f}
	m.DF = int(msg[0] >> 3)
	syn := modesSyndrome(msg, bits)
	switch m.DF {
	case 17, 18:
		if syn != 0 {
			return nil, fmt.Errorf("DF%d CRC mismatch (syndrome %06X)", m.DF, syn)
		}
		m.ICAO24 = strings.ToUpper(f[2:8]) // AA field
	case 11:
		m.ICAO24 = fmt.Sprintf("%06X", syn) // address recovered from parity
	default:
		// Address parity: the syndrome is the ICAO24.
		m.ICAO24 = fmt.Sprintf("%06X", syn)
	}
	if m.DF != 17 && m.DF != 18 {
		return m, nil
	}
	// Extended squitter: ME field is bytes 4..10.
	me := msg[4:11]
	tc := int(me[0] >> 3)
	m.TC = tc
	switch {
	case tc >= 1 && tc <= 4:
		var cs strings.Builder
		for i := 0; i < 8; i++ {
			cs.WriteByte(flightChar(getbits(me, 8+i*6, 6)))
		}
		m.Callsign = strings.TrimSpace(cs.String())
	case tc >= 9 && tc <= 18:
		// Airborne position with barometric altitude.
		ac12 := getbits(me, 8, 12)
		if q := ac12 & 0x10; q != 0 {
			n := ((ac12 & 0xFE0) >> 1) | (ac12 & 0x00F)
			m.Altitude = int(n)*25 - 1000
			m.HasAlt = true
		}
		m.CPROdd = getbits(me, 21, 1) == 1
		m.CPRLat = float64(getbits(me, 22, 17)) / 131072.0
		m.CPRLon = float64(getbits(me, 39, 17)) / 131072.0
		m.HasCPR = true
	case tc >= 5 && tc <= 8:
		// Surface position. The ME layout is: movement at bits 5..11,
		// ground-track status at bit 12, ground track at bits 13..19,
		// then the time flag (20) and CPR format (21) before the CPR
		// latitude/longitude. (The airborne altitude offsets do not
		// apply here.)
		mv := int(getbits(me, 5, 7))
		m.GroundSpeedKt = surfaceSpeed(mv)
		if getbits(me, 12, 1) == 1 {
			m.SurfaceTrack = float64(getbits(me, 13, 7)) * 360.0 / 128.0
		}
		m.CPROdd = getbits(me, 21, 1) == 1
		m.CPRLat = float64(getbits(me, 22, 17)) / 131072.0
		m.CPRLon = float64(getbits(me, 39, 17)) / 131072.0
		m.HasCPR = true
		m.HasSurface = true
	case tc == 19:
		sub := int(me[0] & 7)
		if sub == 1 || sub == 2 {
			// Ground speed subtypes: east-west and north-south velocities.
			// A velocity field of 0 means the information is not
			// available; otherwise the value is offset by 1. Subtype 2 is
			// the supersonic scale, 4 kt per count.
			ewRaw := int64(getbits(me, 14, 10))
			nsRaw := int64(getbits(me, 25, 10))
			if ewRaw != 0 && nsRaw != 0 {
				scale := float64(1)
				if sub == 2 {
					scale = 4
				}
				ew := float64(ewRaw - 1)
				ns := float64(nsRaw - 1)
				if getbits(me, 13, 1) == 1 {
					ew = -ew
				}
				if getbits(me, 24, 1) == 1 {
					ns = -ns
				}
				m.SpeedKt = math.Hypot(ew, ns) * scale
				hdg := math.Atan2(ew, ns) * 180 / math.Pi
				if hdg < 0 {
					hdg += 360
				}
				m.TrackDeg = hdg
				m.VertRate = tc19VertRate(me)
				m.HasVel = true
			}
		} else if sub == 3 || sub == 4 {
			// Airspeed subtypes: heading plus a single airspeed field,
			// again 0 = not available and offset by 1.
			asRaw := int64(getbits(me, 25, 10))
			if asRaw != 0 {
				if getbits(me, 13, 1) == 1 {
					m.TrackDeg = float64(getbits(me, 14, 10)) * 360.0 / 1024.0
				}
				scale := float64(1)
				if sub == 4 {
					scale = 4
				}
				m.SpeedKt = float64(asRaw-1) * scale
				m.VertRate = tc19VertRate(me)
				m.HasVel = true
			}
		}
	}
	m.Summary = m.Describe()
	return m, nil
}

// tc19VertRate decodes the TC19 vertical-rate field (ME bits 36..45).
// A zero field means no vertical-rate information, reported as 0.
func tc19VertRate(me []byte) int {
	raw := int64(getbits(me, 37, 9))
	if raw == 0 {
		return 0
	}
	vr := raw - 1
	if getbits(me, 36, 1) == 1 {
		vr = -vr
	}
	return int(vr) * 64
}

// surfaceSpeed maps the surface movement code to knots.
func surfaceSpeed(mv int) float64 {
	switch {
	case mv == 0:
		return 0
	case mv == 1:
		return 0
	case mv <= 8:
		return 0.125 * float64(mv-1)
	case mv <= 12:
		return 1 + 0.25*float64(mv-9)
	case mv <= 38:
		return 2 + 0.5*float64(mv-13)
	case mv <= 93:
		return 15 + float64(mv-39)
	case mv <= 108:
		return 70 + 2*float64(mv-94)
	case mv <= 123:
		return 100 + 5*float64(mv-109)
	default:
		return 175
	}
}

// Describe renders a one-line summary.
func (m *ModeSMessage) Describe() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "df=%d icao=%s", m.DF, m.ICAO24)
	if m.TC != 0 {
		fmt.Fprintf(&sb, " tc=%d", m.TC)
	}
	if m.Callsign != "" {
		fmt.Fprintf(&sb, " callsign=%s", m.Callsign)
	}
	if m.HasAlt {
		fmt.Fprintf(&sb, " alt=%dft", m.Altitude)
	}
	if m.HasVel {
		fmt.Fprintf(&sb, " gs=%.0fkt track=%.1f vr=%dfpm", m.SpeedKt, m.TrackDeg, m.VertRate)
	}
	if m.HasSurface {
		fmt.Fprintf(&sb, " surface gs=%.1fkt track=%.1f", m.GroundSpeedKt, m.SurfaceTrack)
	}
	if m.HasCPR {
		fmt.Fprintf(&sb, " cpr(%s)=%.6f,%.6f", map[bool]string{true: "odd", false: "even"}[m.CPROdd], m.CPRLat, m.CPRLon)
	}
	return sb.String()
}

// cprNL is the longitude-zone count for CPR at the given latitude.
func cprNL(lat float64) int {
	a := math.Abs(lat)
	switch {
	case a < 10.47047130:
		return 59
	case a < 14.82817437:
		return 58
	case a < 18.18626357:
		return 57
	case a < 21.02939493:
		return 56
	case a < 23.54504487:
		return 55
	case a < 25.82924707:
		return 54
	case a < 27.93898710:
		return 53
	case a < 29.91135686:
		return 52
	case a < 31.77209708:
		return 51
	case a < 33.53993436:
		return 50
	case a < 35.22899598:
		return 49
	case a < 36.85025108:
		return 48
	case a < 38.41241892:
		return 47
	case a < 39.92256684:
		return 46
	case a < 41.38651832:
		return 45
	case a < 42.80914012:
		return 44
	case a < 44.19454951:
		return 43
	case a < 45.54626723:
		return 42
	case a < 46.86733252:
		return 41
	case a < 48.16039128:
		return 40
	case a < 49.42776439:
		return 39
	case a < 50.67150166:
		return 38
	case a < 51.89342469:
		return 37
	case a < 53.09516153:
		return 36
	case a < 54.27817472:
		return 35
	case a < 55.44378444:
		return 34
	case a < 56.59318756:
		return 33
	case a < 57.72747354:
		return 32
	case a < 58.84763776:
		return 31
	case a < 59.95459277:
		return 30
	case a < 61.04917774:
		return 29
	case a < 62.13216659:
		return 28
	case a < 63.20427479:
		return 27
	case a < 64.26616523:
		return 26
	case a < 65.31845310:
		return 25
	case a < 66.36171008:
		return 24
	case a < 67.39646774:
		return 23
	case a < 68.42322022:
		return 22
	case a < 69.44242631:
		return 21
	case a < 70.45451075:
		return 20
	case a < 71.45986473:
		return 19
	case a < 72.45884545:
		return 18
	case a < 73.45177442:
		return 17
	case a < 74.43893416:
		return 16
	case a < 75.42056257:
		return 15
	case a < 76.39684391:
		return 14
	case a < 77.36789461:
		return 13
	case a < 78.33374083:
		return 12
	case a < 79.29428225:
		return 11
	case a < 80.24923213:
		return 10
	case a < 81.19801349:
		return 9
	case a < 82.13956981:
		return 8
	case a < 83.07199445:
		return 7
	case a < 83.99173563:
		return 6
	case a < 84.89166191:
		return 5
	case a < 85.75541621:
		return 4
	case a < 86.53536998:
		return 3
	case a < 87.0:
		return 2
	default:
		return 1
	}
}

func cprMod(a, b int) int {
	r := a % b
	if r < 0 {
		r += b
	}
	return r
}

// CPRPosition globally decodes one even and one odd airborne CPR frame from
// the same aircraft. evenWhen/oddWhen are the observation times of the two
// frames; the position is reported at the newer frame's latitude band.
func CPRPosition(even, odd *ModeSMessage, evenWhen, oddWhen time.Time) (lat, lon float64, err error) {
	if !even.HasCPR || !odd.HasCPR || even.CPROdd || !odd.CPROdd {
		return 0, 0, fmt.Errorf("need one even and one odd CPR frame")
	}
	const dlatEven, dlatOdd = 360.0 / 60.0, 360.0 / 59.0
	j := int(math.Floor(59*even.CPRLat - 60*odd.CPRLat + 0.5))
	latEven := dlatEven * float64(cprMod(j, 60))
	latEven += dlatEven * even.CPRLat
	latOdd := dlatOdd*float64(cprMod(j, 59)) + dlatOdd*odd.CPRLat
	if latEven >= 270 {
		latEven -= 360
	}
	if latOdd >= 270 {
		latOdd -= 360
	}
	if cprNL(latEven) != cprNL(latOdd) {
		return 0, 0, fmt.Errorf("CPR frames straddle a latitude zone boundary")
	}
	if !evenWhen.Before(oddWhen) {
		// Even frame is newer.
		ni := cprNL(latEven)
		if ni < 1 {
			ni = 1
		}
		m := int(math.Floor(even.CPRLon*float64(ni-1) - odd.CPRLon*float64(ni) + 0.5))
		lon = (360.0 / float64(ni)) * float64(cprMod(m, ni))
		lon += (360.0 / float64(ni)) * even.CPRLon
		lat = latEven
	} else {
		ni := cprNL(latOdd) - 1
		if ni < 1 {
			ni = 1
		}
		m := int(math.Floor(even.CPRLon*float64(cprNL(latOdd)-1) - odd.CPRLon*float64(cprNL(latOdd)) + 0.5))
		lon = (360.0 / float64(ni)) * float64(cprMod(m, ni))
		lon += (360.0 / float64(ni)) * odd.CPRLon
		lat = latOdd
	}
	if lon > 180 {
		lon -= 360
	}
	return lat, lon, nil
}
