package decode

import (
	"encoding/hex"
	"strings"
)

// blob.go: parse the raw frame blobs SatNOGS stations upload as
// "demodulated data" for an observation. Those files are the output of
// the station's own demodulator (gr-satellites, direwolf, a hardware
// TNC), so they arrive either as one or more raw AX.25 frames back to
// back - flag bytes stripped, FCS usually present - or as a hex dump
// with one frame per line. ParseBlobFrames handles both and reports the
// CRC status of every frame so callers can keep "station decoded" and
// "CRC verified here" distinct.

// BlobFrame is one frame recovered from a demodulated-data blob.
type BlobFrame struct {
	AX25
	// CRCValid reports whether the frame's trailing two bytes are a
	// correct AX.25 FCS. False means the frame was parsed leniently:
	// either the station stripped the FCS or the bytes are not AX.25 at
	// all, so treat the content with care.
	CRCValid bool
	// Offset is the byte position in the blob where the frame started.
	Offset int
}

// ParseBlobFrames extracts every AX.25 frame it can from a demodulated
// data blob, CRC-checked frames first.
func ParseBlobFrames(data []byte) []BlobFrame {
	if frames := blobHexLines(data); len(frames) > 0 {
		return frames
	}
	if frames := blobRawScan(data); len(frames) > 0 {
		return frames
	}
	// Lenient last resort: one frame, no usable FCS. Only accept it when
	// the header parses cleanly, so random binary does not become a
	// phantom packet.
	if ax, ok := parseAX25Lenient(data); ok {
		return []BlobFrame{{AX25: ax, CRCValid: false, Offset: 0}}
	}
	return nil
}

// blobHexLines handles text blobs: lines of hex, optionally prefixed
// with offsets or timestamps the way gr-satellites and loggers write
// them. A line is taken as hex when, stripped of everything that is not
// a hex digit, it still parses to a plausible frame.
func blobHexLines(data []byte) []BlobFrame {
	text := string(data)
	for _, r := range text {
		if r > 0x7E || (r < 0x20 && r != '\n' && r != '\r' && r != '\t') {
			return nil // not text at all
		}
	}
	var out []BlobFrame
	for _, line := range strings.Split(text, "\n") {
		var digits strings.Builder
		for _, r := range line {
			switch {
			case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
				digits.WriteRune(r)
			}
		}
		s := digits.String()
		if len(s) < 28 || len(s)%2 != 0 {
			continue
		}
		raw, err := hex.DecodeString(s)
		if err != nil {
			continue
		}
		if ax, ok := parseAX25(raw); ok && crc16(raw) == 0xF0B8 {
			out = append(out, BlobFrame{AX25: ax, CRCValid: true})
			continue
		}
		if ax, ok := parseAX25Lenient(raw); ok {
			out = append(out, BlobFrame{AX25: ax, CRCValid: false})
		}
	}
	return out
}

// blobRawScan walks a binary blob looking for CRC-valid AX.25 frames.
// Frames carry no length field, so at each offset every plausible length
// is tried against the FCS residue; a hit consumes the frame and the
// scan continues after it.
func blobRawScan(data []byte) []BlobFrame {
	const minFrame = 17 // 14 header + control + pid + 2 FCS
	var out []BlobFrame
	for off := 0; off+minFrame <= len(data); off++ {
		matched := false
		for l := minFrame; l <= 330 && off+l <= len(data); l++ {
			if crc16(data[off:off+l]) != 0xF0B8 {
				continue
			}
			if ax, ok := parseAX25(data[off : off+l]); ok {
				out = append(out, BlobFrame{AX25: ax, CRCValid: true, Offset: off})
				off += l - 1 // -1: the loop's off++ moves past the frame
				matched = true
			}
			break // length is settled by the CRC, valid frame or not
		}
		_ = matched
	}
	return out
}

// parseAX25Lenient parses a frame whose FCS is missing or unchecked.
// Appending two dummy bytes makes parseAX25 drop them instead of real
// payload, so the information field comes back intact.
func parseAX25Lenient(frame []byte) (AX25, bool) {
	padded := append(append([]byte(nil), frame...), 0, 0)
	return parseAX25(padded)
}
