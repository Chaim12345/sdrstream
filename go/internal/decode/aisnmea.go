package decode

// AIS (ITU-R M.1371) message decoding from raw NMEA 0183 !AIVDM/!AIVDO
// sentences, as carried by open community AIS feeds (AISHub, openwaters.io,
// Digitraffic and AIS-catcher community networks).
//
// A sentence is
//
//	!AIVDM,<fragments>,<fragment number>,<sequential id>,<channel>,<payload>,<fill bits>*<checksum>
//
// The payload is 6-bit armoured: each character carries six bits, most
// significant bit first. Decoding here is original code; the bit layouts
// follow the public ITU-R M.1371 recommendation.

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// AISMessage is one decoded AIS message. Which fields are populated depends
// on Type.
type AISMessage struct {
	Type        int     `json:"type"`
	Repeat      int     `json:"repeat"`
	MMSI        int     `json:"mmsi"`
	NavStatus   int     `json:"nav_status,omitempty"`
	RateOfTurn  float64 `json:"rot,omitempty"` // deg/min; NaN when unavailable
	SOG         float64 `json:"sog,omitempty"` // knots
	Accuracy    bool    `json:"accuracy,omitempty"`
	Lat         float64 `json:"lat,omitempty"`
	Lon         float64 `json:"lon,omitempty"`
	HasPos      bool    `json:"has_pos,omitempty"`
	COG         float64 `json:"cog,omitempty"` // degrees
	Heading     int     `json:"heading,omitempty"`
	Second      int     `json:"second,omitempty"`
	Altitude    int     `json:"altitude,omitempty"` // type 9 SAR aircraft, metres
	IMO         int     `json:"imo,omitempty"`
	Callsign    string  `json:"callsign,omitempty"`
	Name        string  `json:"name,omitempty"`
	ShipType    int     `json:"ship_type,omitempty"`
	AidType     int     `json:"aid_type,omitempty"`
	DimBow      int     `json:"dim_bow,omitempty"`
	DimStern    int     `json:"dim_stern,omitempty"`
	DimPort     int     `json:"dim_port,omitempty"`
	DimStar     int     `json:"dim_star,omitempty"`
	ETAMonth    int     `json:"eta_month,omitempty"`
	ETADay      int     `json:"eta_day,omitempty"`
	ETAHour     int     `json:"eta_hour,omitempty"`
	ETAMinute   int     `json:"eta_minute,omitempty"`
	Draught     float64 `json:"draught,omitempty"`
	Destination string  `json:"destination,omitempty"`
	VirtualAid  bool    `json:"virtual_aid,omitempty"`
	Assigned    bool    `json:"assigned,omitempty"`
	Raw         string  `json:"raw,omitempty"` // the NMEA sentence(s) this was decoded from
}

// bitReader reads MSB-first bits from a de-armoured payload.
type bitReader struct {
	data  []byte // six usable bits per byte, value 0..63
	nbits int
}

// unarmour converts the AIS payload characters to their six-bit values.
func unarmour(payload string) ([]byte, error) {
	out := make([]byte, len(payload))
	for i := 0; i < len(payload); i++ {
		v := int(payload[i]) - 48
		if v > 40 {
			v -= 8
		}
		if v < 0 || v > 63 {
			return nil, fmt.Errorf("bad AIS payload character %q", payload[i])
		}
		out[i] = byte(v)
	}
	return out, nil
}

// u extracts n bits starting at bit offset start as an unsigned integer.
func (b *bitReader) u(start, n int) uint64 {
	var v uint64
	for i := 0; i < n; i++ {
		p := start + i
		if p >= b.nbits {
			return v << uint(n-i) // zero-pad short reads at the tail
		}
		bit := (b.data[p/6] >> uint(5-p%6)) & 1
		v = v<<1 | uint64(bit)
	}
	return v
}

// s extracts n bits as a signed (two's complement) integer.
func (b *bitReader) s(start, n int) int64 {
	v := b.u(start, n)
	if n > 0 && v>>(uint(n-1))&1 == 1 {
		return int64(v) - (1 << uint(n))
	}
	return int64(v)
}

// str extracts 6-bit ASCII text: values 0..31 map to '@'..'_', 32..63 map
// to ' '..'?'. Trailing '@' padding and spaces are trimmed.
func (b *bitReader) str(start, n int) string {
	var sb strings.Builder
	for i := 0; i < n/6; i++ {
		v := b.u(start+i*6, 6)
		var c byte
		if v < 32 {
			c = byte(v) + 64
		} else {
			c = byte(v)
		}
		sb.WriteByte(c)
	}
	return strings.TrimRight(sb.String(), "@ ")
}

// checksumOK validates the NMEA checksum: XOR of bytes between '!' and '*'.
func checksumOK(sentence string) bool {
	st := strings.IndexByte(sentence, '*')
	if st < 0 || st+3 > len(sentence) {
		return false
	}
	want, err := strconv.ParseUint(sentence[st+1:st+3], 16, 8)
	if err != nil {
		return false
	}
	body := sentence[1:st]
	var sum byte
	for i := 0; i < len(body); i++ {
		sum ^= body[i]
	}
	return sum == byte(want)
}

// nmeaFields splits an !AIVDM sentence into its comma-separated fields,
// after verifying the checksum.
func nmeaFields(sentence string) ([]string, error) {
	s := strings.TrimSpace(sentence)
	if len(s) < 3 || s[0] != '!' {
		return nil, errors.New("not an NMEA sentence")
	}
	if !checksumOK(s) {
		return nil, errors.New("NMEA checksum mismatch")
	}
	st := strings.IndexByte(s, '*')
	return strings.Split(s[:st], ","), nil
}

// AISAssembler reassembles multi-sentence AIS messages. It is safe for
// concurrent use.
type AISAssembler struct {
	mu     sync.Mutex
	parts  map[string]map[int]string // key: channel+seq id -> fragment number -> payload
	fill   map[string]int
	total  map[string]int
	rawLog map[string][]string
}

func NewAISAssembler() *AISAssembler {
	return &AISAssembler{
		parts:  make(map[string]map[int]string),
		fill:   make(map[string]int),
		total:  make(map[string]int),
		rawLog: make(map[string][]string),
	}
}

// Feed consumes one NMEA sentence. When it completes a message it returns
// the assembled bit reader and the raw sentences; otherwise it returns nil.
func (a *AISAssembler) Feed(sentence string) (*bitReader, string, error) {
	fields, err := nmeaFields(sentence)
	if err != nil {
		return nil, "", err
	}
	if len(fields) != 7 {
		return nil, "", fmt.Errorf("expected 7 fields, got %d", len(fields))
	}
	if fields[0] != "!AIVDM" && fields[0] != "!AIVDO" {
		return nil, "", fmt.Errorf("unsupported sentence type %q", fields[0])
	}
	nfrag, err1 := strconv.Atoi(fields[1])
	fnum, err2 := strconv.Atoi(fields[2])
	if err1 != nil || err2 != nil || nfrag < 1 || fnum < 1 || fnum > nfrag {
		return nil, "", fmt.Errorf("bad fragment numbers %q/%q", fields[1], fields[2])
	}
	seqID, channel := fields[3], fields[4]
	payload := fields[5]
	fill, _ := strconv.Atoi(fields[6])
	if nfrag == 1 {
		arm, err := unarmour(payload)
		if err != nil {
			return nil, "", err
		}
		return &bitReader{data: arm, nbits: len(arm)*6 - fill}, sentence, nil
	}

	key := channel + "|" + seqID
	a.mu.Lock()
	if a.parts[key] == nil {
		a.parts[key] = make(map[int]string)
		a.total[key] = nfrag
		a.rawLog[key] = make([]string, 0, nfrag)
	}
	if a.total[key] != nfrag || len(a.parts[key]) >= nfrag {
		// A reused sequence id or a stutter: start over for this key.
		a.parts[key] = make(map[int]string)
		a.total[key] = nfrag
		a.rawLog[key] = a.rawLog[key][:0]
	}
	a.parts[key][fnum] = payload
	if fnum == nfrag {
		a.fill[key] = fill
	}
	a.rawLog[key] = append(a.rawLog[key], sentence)
	if len(a.parts[key]) < nfrag {
		a.mu.Unlock()
		return nil, "", nil
	}
	var sb strings.Builder
	for i := 1; i <= nfrag; i++ {
		p, ok := a.parts[key][i]
		if !ok {
			a.mu.Unlock()
			return nil, "", nil // gap; wait for the next cycle
		}
		sb.WriteString(p)
	}
	raw := strings.Join(a.rawLog[key], "\n")
	totbits := len(sb.String())*6 - a.fill[key]
	delete(a.parts, key)
	delete(a.fill, key)
	delete(a.total, key)
	delete(a.rawLog, key)
	a.mu.Unlock()
	arm, err := unarmour(sb.String())
	if err != nil {
		return nil, "", err
	}
	return &bitReader{data: arm, nbits: totbits}, raw, nil
}

// DecodeAIS decodes one complete AIS message (after reassembly).
func DecodeAIS(b *bitReader, raw string) (*AISMessage, error) {
	if b.nbits < 38 {
		return nil, fmt.Errorf("AIS message too short: %d bits", b.nbits)
	}
	m := &AISMessage{Raw: raw}
	m.Type = int(b.u(0, 6))
	m.Repeat = int(b.u(6, 2))
	m.MMSI = int(b.u(8, 30))
	switch m.Type {
	case 1, 2, 3:
		m.decodePosition(b)
	case 5:
		m.decodeStatic(b)
	case 9:
		m.decodeSARAircraft(b)
	case 18:
		m.decodeClassB(b)
	case 19:
		m.decodeClassBExtended(b)
	case 21:
		m.decodeAidToNav(b)
	case 24:
		m.decodeClassBStatic(b)
	}
	return m, nil
}

// decodePosition handles message types 1-3 (class A position reports).
func (m *AISMessage) decodePosition(b *bitReader) {
	m.NavStatus = int(b.u(38, 4))
	rot := b.s(42, 8)
	if rot == -128 {
		m.RateOfTurn = 0 // unavailable; reported as 0 with Heading sentinel
	} else {
		sign := 1.0
		if rot < 0 {
			sign = -1
		}
		x := float64(rot) / 4.733
		m.RateOfTurn = sign * x * x
	}
	sog := b.u(50, 10)
	if sog != 1023 {
		m.SOG = float64(sog) / 10
	}
	m.Accuracy = b.u(60, 1) == 1
	m.decodeLatLon(b, 61)
	cog := b.u(116, 12)
	if cog != 3600 {
		m.COG = float64(cog) / 10
	}
	m.Heading = int(b.u(128, 9))
	m.Second = int(b.u(137, 6))
}

func (m *AISMessage) decodeLatLon(b *bitReader, start int) {
	lon := b.s(start, 28)
	lat := b.s(start+28, 27)
	if lon != 0x6791AC0 && lat != 0x3412140 { // 181/91 degrees = unavailable
		m.Lon = float64(lon) / 600000
		m.Lat = float64(lat) / 600000
		m.HasPos = true
	}
}

// decodeStatic handles message type 5 (ship static and voyage data).
func (m *AISMessage) decodeStatic(b *bitReader) {
	m.IMO = int(b.u(40, 30))
	m.Callsign = b.str(70, 42)
	m.Name = b.str(112, 120)
	m.ShipType = int(b.u(232, 8))
	m.DimBow = int(b.u(240, 9))
	m.DimStern = int(b.u(249, 9))
	m.DimPort = int(b.u(258, 6))
	m.DimStar = int(b.u(264, 6))
	m.ETAMonth = int(b.u(274, 4))
	m.ETADay = int(b.u(278, 5))
	m.ETAHour = int(b.u(283, 5))
	m.ETAMinute = int(b.u(288, 6))
	m.Draught = float64(b.u(294, 8)) / 10
	m.Destination = b.str(302, 120)
}

// decodeSARAircraft handles message type 9 (search and rescue aircraft).
func (m *AISMessage) decodeSARAircraft(b *bitReader) {
	alt := b.u(38, 12)
	if alt != 4095 {
		m.Altitude = int(alt)
	}
	sog := b.u(50, 10)
	if sog != 1023 {
		m.SOG = float64(sog) // whole knots for SAR aircraft
	}
	m.Accuracy = b.u(60, 1) == 1
	m.decodeLatLon(b, 61)
	cog := b.u(116, 12)
	if cog != 3600 {
		m.COG = float64(cog) / 10
	}
	m.Second = int(b.u(128, 6))
}

// decodeClassB handles message type 18 (class B position report).
func (m *AISMessage) decodeClassB(b *bitReader) {
	sog := b.u(46, 10)
	if sog != 1023 {
		m.SOG = float64(sog) / 10
	}
	m.Accuracy = b.u(56, 1) == 1
	m.decodeLatLon(b, 57)
	cog := b.u(112, 12)
	if cog != 3600 {
		m.COG = float64(cog) / 10
	}
	m.Heading = int(b.u(124, 9))
	m.Second = int(b.u(133, 6))
	m.Assigned = b.u(146, 1) == 1
}

// decodeClassBExtended handles message type 19.
func (m *AISMessage) decodeClassBExtended(b *bitReader) {
	sog := b.u(46, 10)
	if sog != 1023 {
		m.SOG = float64(sog) / 10
	}
	m.Accuracy = b.u(56, 1) == 1
	m.decodeLatLon(b, 57)
	cog := b.u(112, 12)
	if cog != 3600 {
		m.COG = float64(cog) / 10
	}
	m.Heading = int(b.u(124, 9))
	m.Second = int(b.u(133, 6))
	m.Name = b.str(143, 120)
	m.ShipType = int(b.u(263, 8))
	m.DimBow = int(b.u(271, 9))
	m.DimStern = int(b.u(280, 9))
	m.DimPort = int(b.u(289, 6))
	m.DimStar = int(b.u(295, 6))
}

// decodeAidToNav handles message type 21 (aids to navigation).
func (m *AISMessage) decodeAidToNav(b *bitReader) {
	m.AidType = int(b.u(38, 5))
	m.Name = b.str(43, 120)
	m.Accuracy = b.u(163, 1) == 1
	m.decodeLatLon(b, 164)
	m.DimBow = int(b.u(219, 9))
	m.DimStern = int(b.u(228, 9))
	m.DimPort = int(b.u(237, 6))
	m.DimStar = int(b.u(243, 6))
	m.Second = int(b.u(253, 6))
	m.VirtualAid = b.u(272, 1) == 1
}

// decodeClassBStatic handles message type 24 (class B static data, A and B).
func (m *AISMessage) decodeClassBStatic(b *bitReader) {
	part := b.u(38, 2)
	if part == 0 {
		m.Name = b.str(40, 120)
	} else {
		m.ShipType = int(b.u(40, 8))
		m.Callsign = b.str(90, 42)
		m.DimBow = int(b.u(132, 9))
		m.DimStern = int(b.u(141, 9))
		m.DimPort = int(b.u(150, 6))
		m.DimStar = int(b.u(156, 6))
	}
}

// Describe renders a one-line human summary of the decoded message.
func (m *AISMessage) Describe() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "type=%d mmsi=%d", m.Type, m.MMSI)
	if m.HasPos {
		fmt.Fprintf(&sb, " lat=%.5f lon=%.5f", m.Lat, m.Lon)
	}
	if m.COG != 0 {
		fmt.Fprintf(&sb, " cog=%.1f", m.COG)
	}
	if m.SOG != 0 {
		fmt.Fprintf(&sb, " sog=%.1fkn", m.SOG)
	}
	if m.Heading != 0 && m.Heading != 511 {
		fmt.Fprintf(&sb, " hdg=%d", m.Heading)
	}
	if m.Altitude != 0 {
		fmt.Fprintf(&sb, " alt=%dm", m.Altitude)
	}
	if m.Name != "" {
		fmt.Fprintf(&sb, " name=%q", m.Name)
	}
	if m.Callsign != "" {
		fmt.Fprintf(&sb, " cs=%q", m.Callsign)
	}
	if m.Destination != "" {
		fmt.Fprintf(&sb, " dest=%q", m.Destination)
	}
	if m.ShipType != 0 {
		fmt.Fprintf(&sb, " shiptype=%d", m.ShipType)
	}
	return sb.String()
}
