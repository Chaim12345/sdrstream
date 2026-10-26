package main

// Standalone Web-888 (RaspSDR fork) IQ packet parser.
//
// Verified against the fork source, NOT against the main-repo Kiwi parser:
// github.com/RaspSDR/server @ 6445a00742e35935a4b2e43453b5a1e3afc7699e
//   rx/rx_sound.h:56-70  snd_pkt_iq_t: id[3] flags seq[4] smeter[2]
//                        last_gps_solution dummy gpssec(u4) gpsnsec(u4)
//                        = 20-byte header, then interleaved s2_t I/Q
//   rx/rx_sound.cpp:473  SND_FLAG_MODE_IQ       = 0x08
//   rx/rx_sound.cpp:477  SND_FLAG_LITTLE_ENDIAN = 0x80
//   rx/rx_sound.cpp:1112-1137 payload int16 BE by default (network order),
//                        native LE only after the client sent
//                        "SET little-endian" (rx_sound_cmd.cpp:344-351) and
//                        the packet carries flag 0x80
// Header multi-byte fields (seq, gpssec, gpsnsec) are ARM-native
// little-endian regardless of payload order - the header is a packed struct
// memcpy'd to the wire and never byte-swapped in either branch.

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const web888IQHeaderLen = 20

const (
	web888FlagModeIQ       = 0x08
	web888FlagLittleEndian = 0x80
)

type web888IQFrame struct {
	Flags   byte
	Seq     uint32
	GpsSec  uint32
	GpsNSec uint32
	Samples []int16 // interleaved I,Q,... in wire order
}

var errWeb888NotIQ = errors.New("web888: SND packet without MODE_IQ flag")

func parseWeb888IQFrame(pkt []byte) (web888IQFrame, error) {
	var f web888IQFrame
	if len(pkt) < web888IQHeaderLen {
		return f, fmt.Errorf("web888: short packet %d < %d", len(pkt), web888IQHeaderLen)
	}
	if string(pkt[:3]) != "SND" {
		return f, fmt.Errorf("web888: bad id %q", pkt[:3])
	}
	f.Flags = pkt[3]
	if f.Flags&web888FlagModeIQ == 0 {
		return f, errWeb888NotIQ
	}
	f.Seq = binary.LittleEndian.Uint32(pkt[4:8])
	f.GpsSec = binary.LittleEndian.Uint32(pkt[12:16])
	f.GpsNSec = binary.LittleEndian.Uint32(pkt[16:20])
	payload := pkt[web888IQHeaderLen:]
	if len(payload)%2 != 0 {
		return f, fmt.Errorf("web888: odd payload %d", len(payload))
	}
	f.Samples = make([]int16, len(payload)/2)
	if f.Flags&web888FlagLittleEndian != 0 {
		for i := range f.Samples {
			f.Samples[i] = int16(binary.LittleEndian.Uint16(payload[2*i:]))
		}
	} else {
		for i := range f.Samples {
			f.Samples[i] = int16(binary.BigEndian.Uint16(payload[2*i:]))
		}
	}
	return f, nil
}
