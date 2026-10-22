// HFNPDU and ACARS message layers.
//
// Ported from dumphfdl hfnpdu.c (GPL-3.0-or-later, Tomasz Lemiech) and
// libacars acars.c (MIT, Tomasz Lemiech). Not ported: ACARS multi-block
// reassembly, system table reassembly, and the application decoders for
// ADS-C, CPDLC, MIAM and ARINC-622. Those payloads are left as raw text.
package hfdl

import (
	"fmt"
	"strings"
)

// HFNPDU types.
const (
	HFNSystemTable        = 0xD0
	HFNPerformanceData    = 0xD1
	HFNSystemTableRequest = 0xD2
	HFNFrequencyData      = 0xD5
	HFNDelayedEcho        = 0xDE
	HFNEnvelopedData      = 0xFF
)

// ACARS is a decoded ACARS block carried in an enveloped HFNPDU.
type ACARS struct {
	CRCOK    bool
	Final    bool // ETX (last block) rather than ETB
	Mode     byte
	Reg      string
	Ack      byte
	Label    string
	BlockID  byte
	MsgNum   string // downlink only
	FlightID string // downlink only
	Sublabel string
	MFI      string
	Text     string
	Downlink bool
}

// HFNPDU is the network layer unit inside a data LPDU.
type HFNPDU struct {
	Type int
	Err  bool

	// Performance data (0xD1) and frequency data (0xD5).
	FlightID string
	Lat, Lon float64
	UTC      string

	// Performance data only.
	GSID       int
	FreqID     int
	FlightLeg  int
	MPDUsRx    [4]int // 1800, 1200, 600, 300 bps
	MPDUsRxErr [4]int

	// Frequency data only.
	PropFreqs []PropFreq

	// System table header.
	SystableVersion int
	SystablePDU     int
	SystableTotal   int

	ACARS *ACARS
}

// PropFreq is one ground station entry of a frequency data HFNPDU.
type PropFreq struct {
	GSID  int
	Prop  uint32 // bitmap of propagating frequency IDs
	Tuned uint32
}

func coord(c uint32) float64 {
	v := int32(c<<12) >> 12 // sign extend 20 bits
	return float64(v) * 180.0 / float64(0x7ffff)
}

func u16(b []byte) int { return int(b[0]) | int(b[1])<<8 }

func utc(t int) string { return fmt.Sprintf("%02d:%02d:%02d", t/3600, t%3600/60, t%60) }

// ParseHFNPDU parses the payload of a data LPDU. Returns nil when the
// payload is not an HFNPDU.
func ParseHFNPDU(b []byte, downlink bool) *HFNPDU {
	if len(b) < 2 || b[0] != 0xFF {
		return nil
	}
	h := &HFNPDU{Type: int(b[1])}
	switch h.Type {
	case HFNSystemTable:
		if len(b) < 5 {
			h.Err = true
			break
		}
		h.SystableTotal = int(b[2]>>4) + 1
		h.SystablePDU = int(b[2] & 0xF)
		h.SystableVersion = int(b[3]>>4) | int(b[4])<<4
	case HFNPerformanceData:
		if len(b) < 47 {
			h.Err = true
			break
		}
		h.FlightID = strings.TrimRight(string(b[2:8]), "\x00 ")
		h.Lat = coord(uint32(b[8]) | uint32(b[9])<<8 | uint32(b[10]&0xF)<<16)
		h.Lon = coord(uint32(b[10]&0xF0)>>4 | uint32(b[11])<<4 | uint32(b[12])<<12)
		h.UTC = utc(2 * u16(b[13:]))
		h.FlightLeg = int(b[16])
		h.GSID = int(b[17] & 0x7F)
		h.FreqID = int(b[18])
		for i := 0; i < 4; i++ {
			h.MPDUsRx[i] = int(b[27+i])
			h.MPDUsRxErr[i] = int(b[31+i])
		}
	case HFNSystemTableRequest:
		h.Err = len(b) < 4
	case HFNFrequencyData:
		if len(b) < 15 {
			h.Err = true
			break
		}
		h.FlightID = strings.TrimRight(string(b[2:8]), "\x00 ")
		h.Lat = coord(uint32(b[8]) | uint32(b[9])<<8 | uint32(b[10]&0xF)<<16)
		h.Lon = coord(uint32(b[10]&0xF0)>>4 | uint32(b[11])<<4 | uint32(b[12])<<12)
		h.UTC = utc(2 * u16(b[13:]))
		for f := 0; f < 6; f++ {
			p := 15 + f*6
			if p+6 > len(b) {
				break
			}
			h.PropFreqs = append(h.PropFreqs, PropFreq{
				GSID:  int(b[p] & 0x7F),
				Prop:  uint32(b[p+1]) | uint32(b[p+2])<<8 | uint32(b[p+3]&0xF)<<16,
				Tuned: uint32(b[p+3]&0xF0)>>4 | uint32(b[p+4])<<4 | uint32(b[p+5])<<12,
			})
		}
	case HFNEnvelopedData:
		h.ACARS = ParseACARS(b[2:], downlink)
	}
	return h
}

// ParseACARS decodes one ACARS block starting at the SOH byte. The CRC is
// checked; a block with a bad CRC is returned with CRCOK false and its text
// must not be trusted. Returns nil when the data is not an ACARS block.
func ParseACARS(b []byte, downlinkDir bool) *ACARS {
	if len(b) < 1 || b[0] != 1 {
		return nil
	}
	b = b[1:]
	if len(b) < 16 || b[len(b)-1] != 0x7f {
		return nil
	}
	b = b[:len(b)-1]
	a := &ACARS{CRCOK: crc16CCITT(b, 0) == 0}
	b = b[:len(b)-2]
	s := make([]byte, len(b))
	for i := range b {
		s[i] = b[i] & 0x7f
	}
	switch s[len(s)-1] {
	case 0x03:
		a.Final = true
	case 0x17:
	default:
		return nil
	}
	s = s[:len(s)-1]
	if len(s) < 12 {
		return nil
	}
	a.Mode = s[0]
	a.Reg = string(s[1:8])
	a.Ack = s[8]
	switch a.Ack {
	case 0x15:
		a.Ack = '!'
	case 0x06:
		a.Ack = '^'
	}
	lab := []byte{s[9], s[10]}
	if lab[1] == 0x7f {
		lab[1] = 'd'
	}
	a.Label = string(lab)
	a.BlockID = s[11]
	if a.BlockID == 0 {
		a.BlockID = ' '
	}
	a.Downlink = a.BlockID >= '0' && a.BlockID <= '9'
	rest := s[12:]
	if len(rest) == 0 {
		if a.Downlink {
			return nil
		}
		return a
	}
	if rest[0] != 0x02 {
		return nil
	}
	rest = append([]byte(nil), rest[1:]...)
	for i := range rest {
		if rest[i] == 0 {
			rest[i] = '.'
		}
	}
	if a.Downlink {
		if len(rest) < 10 {
			return nil
		}
		a.MsgNum = string(rest[:3])
		a.FlightID = string(rest[4:10])
		rest = rest[10:]
	}
	if a.Label == "H1" {
		t := string(rest)
		if !a.Downlink && len(t) >= 5 && strings.HasPrefix(t, "- #") {
			a.Sublabel = t[3:5]
			t = t[5:]
		} else if a.Downlink && len(t) >= 4 && t[0] == '#' && t[3] == 'B' {
			a.Sublabel = t[1:3]
			t = t[4:]
		}
		if a.Sublabel != "" && len(t) >= 4 && t[0] == '/' && t[3] == ' ' {
			a.MFI = t[1:3]
			t = t[4:]
		}
		rest = []byte(t)
	}
	a.Text = string(rest)
	return a
}

// Describe renders a verified frame as human-readable lines.
func Describe(d DecodedFrame) []string {
	f, p := d.Frame, d.PDU
	slot := "single"
	if f.Double {
		slot = "double"
	}
	out := []string{fmt.Sprintf("%d bps %s slot, %s src=%d dst=%d train-BER=%.0f%% freq-err=%.0f Hz",
		f.BitRate(), slot, p.Kind+" "+string(p.Direction), p.SrcID, p.DstID, f.TrainBER*100, f.FreqErr)}
	for _, l := range p.LPDUs {
		line := fmt.Sprintf("  LPDU type 0x%02X", l.Type)
		if l.ICAO != 0 {
			line += fmt.Sprintf(" icao=%06X", l.ICAO)
		}
		out = append(out, line)
		h := ParseHFNPDU(l.Payload, p.Direction == Downlink)
		if h == nil || h.Err {
			continue
		}
		switch h.Type {
		case HFNPerformanceData, HFNFrequencyData:
			out = append(out, fmt.Sprintf("  HFNPDU 0x%02X flight=%s pos=%.3f,%.3f utc=%s", h.Type, h.FlightID, h.Lat, h.Lon, h.UTC))
		case HFNEnvelopedData:
			if a := h.ACARS; a != nil {
				out = append(out, fmt.Sprintf("  ACARS reg=%s label=%s blk=%c crc=%v flight=%s text=%q", strings.TrimLeft(a.Reg, "."), a.Label, a.BlockID, a.CRCOK, a.FlightID, a.Text))
			}
		default:
			out = append(out, fmt.Sprintf("  HFNPDU 0x%02X", h.Type))
		}
	}
	return out
}
