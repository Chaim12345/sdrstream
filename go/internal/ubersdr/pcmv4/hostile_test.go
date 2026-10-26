package pcmv4

import (
	"encoding/binary"
	"testing"
)

func hostilePacket(flags byte, count, rate uint64, body []byte) []byte {
	b := binary.LittleEndian.AppendUint32(nil, PCMv4Magic)
	b = append(b, flags)
	b = append(b, make([]byte, 8)...) // absolute timestamp
	b = binary.AppendUvarint(b, count)
	b = binary.AppendUvarint(b, rate)
	b = append(b, 2) // channels
	return append(b, body...)
}

// A packet can claim anything in its header. Counts and rates outside what a
// real server sends must be refused before any sample buffer is allocated.
func TestHostileHeaderRejected(t *testing.T) {
	const silent = pcmv4FlagMetadata | pcmv4FlagCount | pcmv4FlagSilent
	if _, _, err := NewPCMv4StreamDecoder().DecodePacket(hostilePacket(silent, 100, 48000, nil)); err != nil {
		t.Fatalf("baseline silent packet: %v", err)
	}
	for name, pkt := range map[string][]byte{
		"count 2^40":     hostilePacket(silent, 1<<40, 48000, nil),
		"count cap+1":    hostilePacket(silent, MaxSampleCount+1, 48000, nil),
		"count 2^64-1":   hostilePacket(silent, ^uint64(0), 48000, nil),
		"rate 0":         hostilePacket(silent, 100, 0, nil),
		"rate 2^40":      hostilePacket(silent, 100, 1<<40, nil),
		"escape no body": hostilePacket(pcmv4FlagMetadata|pcmv4FlagCount|pcmv4FlagEscape, MaxSampleCount, 48000, nil),
		"escape short":   hostilePacket(pcmv4FlagMetadata|pcmv4FlagCount|pcmv4FlagEscape, MaxSampleCount, 48000, make([]byte, 64)),
	} {
		if _, s, err := NewPCMv4StreamDecoder().DecodePacket(pkt); err == nil {
			t.Errorf("%s: accepted (%d samples)", name, len(s))
		}
	}
}
