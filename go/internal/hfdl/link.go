package hfdl

// link.go parses the HFDL link layer (SPDU, MPDU, LPDU) from a decoded,
// byte-reversed frame. The structure follows dumphfdl (Tomasz Lemiech,
// GPL-3.0-or-later): src/pdu.c, mpdu.c, lpdu.c, spdu.c, crc.c.

import "fmt"

// Direction of a PDU.
type Direction string

const (
	Uplink   Direction = "uplink"
	Downlink Direction = "downlink"
)

// LPDU types.
const (
	LPDUUnnumberedData      = 0x0D
	LPDUUnnumberedAckedData = 0x1D
	LPDULogonDenied         = 0x2F
	LPDULogoffRequest       = 0x3F
	LPDULogonResume         = 0x4F
	LPDULogonResumeConfirm  = 0x5F
	LPDULogonRequestNormal  = 0x8F
	LPDULogonConfirm        = 0x9F
	LPDULogonRequestDLS     = 0xBF
	spduLen                 = 66
	spduHeaderLen           = 64
)

// LPDU is one link protocol data unit.
type LPDU struct {
	Type    int
	CRCOK   bool
	ICAO    uint32 // logon/logoff PDUs
	ACID    uint8  // logon confirm
	Payload []byte // bytes after the LPDU header (an HFNPDU for data LPDUs)
	Raw     []byte
}

// PDU is one decoded HFDL frame.
type PDU struct {
	Kind      string // "mpdu" or "spdu"
	Direction Direction
	CRCOK     bool
	SrcID     uint8
	DstID     uint8
	LPDUs     []LPDU
	Freq      int
}

// crc16CCITT is the reflected CRC-16/CCITT used by HFDL (poly 0x1021).
func crc16CCITT(data []byte, crc uint16) uint16 {
	for _, b := range data {
		crc ^= uint16(b)
		for i := 0; i < 8; i++ {
			if crc&1 != 0 {
				crc = (crc >> 1) ^ 0x8408
			} else {
				crc >>= 1
			}
		}
	}
	return crc
}

// fcsOK checks the two FCS octets that follow hdrLen header octets.
func fcsOK(buf []byte, hdrLen int) bool {
	if len(buf) < hdrLen+2 {
		return false
	}
	check := uint16(buf[hdrLen]) | uint16(buf[hdrLen+1])<<8
	return crc16CCITT(buf[:hdrLen], 0xFFFF)^0xFFFF == check
}

func icao(b []byte) uint32 { return uint32(b[0])<<16 | uint32(b[1])<<8 | uint32(b[2]) }

// ParsePDU parses a frame as delivered by the demodulator. A frame that
// fails its FCS is returned with CRCOK false and no LPDUs: nothing in it
// may be trusted.
func ParsePDU(buf []byte, freq int) PDU {
	pdu := PDU{Freq: freq, Kind: "mpdu"}
	if len(buf) == 0 {
		return pdu
	}
	// SPDUs are 66 octets with the FCS at 64; they carry no direction
	// bit like MPDUs do, so try the MPDU parse first and fall back.
	if p, ok := parseMPDU(buf, freq); ok {
		return p
	}
	if len(buf) >= spduLen && fcsOK(buf, spduHeaderLen) {
		pdu.Kind = "spdu"
		pdu.CRCOK = true
		pdu.Direction = Uplink
		pdu.SrcID = buf[1] & 0x7F
		return pdu
	}
	return pdu
}

func parseMPDU(buf []byte, freq int) (PDU, bool) {
	pdu := PDU{Freq: freq, Kind: "mpdu"}
	var lpduCnt, hdrLen, aircraft int
	if buf[0]&0x2 != 0 {
		pdu.Direction = Downlink
		lpduCnt = int(buf[0]>>2) & 0xF
		hdrLen = 6 + lpduCnt
	} else {
		pdu.Direction = Uplink
		aircraft = int(buf[0]&0x70>>4) + 1
		hdrLen = 2
		for i := 0; i < aircraft; i++ {
			if len(buf) < hdrLen+2 {
				return pdu, false
			}
			lpduCnt = int(buf[hdrLen+1] >> 4)
			hdrLen += 2 + lpduCnt
		}
	}
	if len(buf) < hdrLen+2 || !fcsOK(buf, hdrLen) {
		return pdu, false
	}
	pdu.CRCOK = true
	data := buf[hdrLen+2:]
	parseList := func(lens []byte, cnt int) bool {
		for j := 0; j < cnt; j++ {
			n := int(lens[j]) + 1
			if n > len(data) {
				return false
			}
			pdu.LPDUs = append(pdu.LPDUs, parseLPDU(data[:n]))
			data = data[n:]
		}
		return true
	}
	if pdu.Direction == Downlink {
		pdu.SrcID = buf[2]
		pdu.DstID = buf[1] & 0x7F
		parseList(buf[6:], lpduCnt)
	} else {
		pdu.SrcID = buf[1] & 0x7F
		pos := 2
		for i := 0; i < aircraft; i++ {
			pdu.DstID = buf[pos]
			cnt := int(buf[pos+1]>>4) & 0xF
			pos += 2
			if !parseList(buf[pos:], cnt) {
				break
			}
			pos += cnt
		}
	}
	return pdu, true
}

func parseLPDU(b []byte) LPDU {
	l := LPDU{Raw: b}
	if len(b) < 3 {
		return l
	}
	body := b[:len(b)-2]
	if !fcsOK(b, len(body)) {
		return l
	}
	l.CRCOK = true
	l.Type = int(body[0])
	consumed := 0
	switch l.Type {
	case LPDUUnnumberedData, LPDUUnnumberedAckedData:
		consumed = 1
	case LPDULogonDenied, LPDULogoffRequest:
		if len(body) >= 5 {
			l.ICAO = icao(body[1:])
			consumed = 5
		}
	case LPDULogonConfirm, LPDULogonResumeConfirm:
		if len(body) >= 8 {
			l.ICAO = icao(body[1:])
			l.ACID = body[4]
			consumed = 8
		}
	case LPDULogonResume, LPDULogonRequestNormal, LPDULogonRequestDLS:
		if len(body) >= 4 {
			l.ICAO = icao(body[1:])
			consumed = 4
		}
	default:
		consumed = len(body)
	}
	if consumed > 0 && consumed < len(body) {
		l.Payload = body[consumed:]
	}
	return l
}

// String summarises a PDU for logs.
func (p PDU) String() string {
	return fmt.Sprintf("%s %s crc=%v src=%d dst=%d lpdus=%d", p.Kind, p.Direction, p.CRCOK, p.SrcID, p.DstID, len(p.LPDUs))
}
