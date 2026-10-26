// KiwiSDR / Web-888 (RaspSDR) SND frame parser for IQ-mode streams.
// Grounded in the owning server source, not reverse-engineered from bytes:
//
//	jks-prv/KiwiSDR rx/rx_sound.h  (snd_pkt_iq_t: 20-byte packed header)
//	jks-prv/KiwiSDR rx/rx_sound.cpp (flags: STEREO 0x08 set for IQ,
//	  LITTLE_ENDIAN 0x80 optional; seq LE u32; smeter BE u16 (dBm+127)*10;
//	  gpssec/gpsnsec LE u32; payload int16 interleaved I/Q, big-endian
//	  unless LITTLE_ENDIAN set)
//	RaspSDR/server (Web-888 fork) rx/rx_sound.{h,cpp} same layout; server
//	  sample rate drives the rate - never assume a fixed 12 kHz.
package iqsrc

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	KiwiSNDHeaderLen   = 20
	kiwiFlagStereo     = 0x08 // set for IQ (and DRM/stereo) packets
	kiwiFlagLittleEnd  = 0x80
	kiwiFlagCompressed = 0x10
)

// KiwiIQHeader is the parsed 20-byte IQ SND packet header.
type KiwiIQHeader struct {
	Flags           uint8
	Seq             uint32
	SmeterRaw       uint16 // (dBm+127)*10, big-endian on the wire
	LastGPSSolution uint8  // seconds since last GPS solution; 253-254 = server error markers
	GPSSec          uint32
	GPSNSec         uint32
	LittleEndian    bool
}

// SmeterDBm returns the s-meter value in dBm.
func (h KiwiIQHeader) SmeterDBm() float64 { return float64(h.SmeterRaw)/10 - 127 }

// ParseKiwiIQFrame validates one binary SND frame as an IQ packet and
// decodes its samples into dst as interleaved complex64 (I in .real, Q in
// .imag), scaled to [-1,1). It returns the header and the number of complex
// samples written. Frames without the IQ/stereo flag, with the compressed
// flag, with a truncated header, or with an odd payload length are errors:
// they are not IQ evidence.
func ParseKiwiIQFrame(data []byte, dst []complex64) (KiwiIQHeader, int, error) {
	var h KiwiIQHeader
	if len(data) < KiwiSNDHeaderLen {
		return h, 0, fmt.Errorf("frame too short for IQ header: %d bytes", len(data))
	}
	if string(data[:3]) != "SND" {
		return h, 0, errors.New("not an SND frame")
	}
	h.Flags = data[3]
	if h.Flags&kiwiFlagStereo == 0 {
		return h, 0, fmt.Errorf("not an IQ packet: STEREO(0x08) flag clear (flags=0x%02x)", h.Flags)
	}
	if h.Flags&kiwiFlagCompressed != 0 {
		return h, 0, fmt.Errorf("compressed flag set on claimed-IQ packet (flags=0x%02x)", h.Flags)
	}
	h.LittleEndian = h.Flags&kiwiFlagLittleEnd != 0
	h.Seq = binary.LittleEndian.Uint32(data[4:8])
	h.SmeterRaw = binary.BigEndian.Uint16(data[8:10])
	h.LastGPSSolution = data[10]
	h.GPSSec = binary.LittleEndian.Uint32(data[12:16])
	h.GPSNSec = binary.LittleEndian.Uint32(data[16:20])
	payload := data[KiwiSNDHeaderLen:]
	if len(payload) == 0 {
		return h, 0, errors.New("IQ packet with empty payload")
	}
	if len(payload)%4 != 0 {
		return h, 0, fmt.Errorf("payload %d bytes not a multiple of one complex int16 pair (4)", len(payload))
	}
	n := len(payload) / 4
	if n > len(dst) {
		n = len(dst)
	}
	if h.LittleEndian {
		for i := 0; i < n; i++ {
			iv := int16(binary.LittleEndian.Uint16(payload[i*4:]))
			qv := int16(binary.LittleEndian.Uint16(payload[i*4+2:]))
			dst[i] = complex(float32(iv)/32768, float32(qv)/32768)
		}
	} else {
		for i := 0; i < n; i++ {
			iv := int16(binary.BigEndian.Uint16(payload[i*4:]))
			qv := int16(binary.BigEndian.Uint16(payload[i*4+2:]))
			dst[i] = complex(float32(iv)/32768, float32(qv)/32768)
		}
	}
	return h, n, nil
}

// IsKiwiIQFrame reports only whether the frame passes the structural IQ
// checks, for counters that do not need the samples.
func IsKiwiIQFrame(data []byte) bool {
	var h KiwiIQHeader
	_, _, err := ParseKiwiIQFrame(data, nil)
	_ = h
	return err == nil
}
