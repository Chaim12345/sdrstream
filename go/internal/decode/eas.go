package decode

import (
	"strings"
)

// EAS (the US Emergency Alert System) sends its alert headers as SAME
// (Specific Area Message Encoding) data: 520.83 baud AFSK, mark 2083.3 Hz
// for a 1 and space 1562.5 Hz for a 0. Each character is seven ASCII bits
// sent least-significant first followed by a null bit, making one byte. A
// transmission is sixteen 0xAB preamble bytes to let receivers sync, then
// "ZCZC-ORG-EEE-PSSCCC+TTTT-JJJHHMM-LLLLLLLL-" and finally "NNNN" to end.
//
// https://www.ecfr.gov/current/title-47/chapter-I/subchapter-A/part-11/subpart-B/section-11.31
const (
	easBaud     = 520.83333
	easMarkHz   = 2083.3
	easSpaceHz  = 1562.5
	easPreamble = 0xAB
)

// EASAlert is one decoded SAME header or end-of-message code.
type EASAlert struct {
	Raw       string // the whole decoded byte string, e.g. ZCZC-...
	Event     string // the three-letter event code (EEE)
	Origin    string // the originator code (ORG)
	Locations []string
	Duration  string
	Timestamp string
	Station   string
	End       bool // true for an NNNN end-of-message code
}

// String renders an alert in the familiar dashed form.
func (a EASAlert) String() string {
	if a.End {
		return "NNNN (end of message)"
	}
	return a.Raw
}

// EASDecode demodulates SAME audio and returns each header or EOM code.
func EASDecode(samples []float64, rate int) []EASAlert {
	if rate < 4000 || len(samples) < rate/4 {
		return nil
	}
	spb := float64(rate) / easBaud
	win := int(spb * 0.8)
	if win < 8 {
		win = 8
	}
	// Per-sample mark/space decision using a window centred on each sample,
	// so a mark->space edge is detected at the sample where the tone really
	// changes. A window that started at the sample would report the edge
	// about half a window early and slip the bit grid.
	half := win / 2
	n := len(samples)
	dec := make([]byte, n)
	for p := half; p+half < n; p++ {
		seg := samples[p-half : p+half]
		if goertzel(seg, rate, easMarkHz) >= goertzel(seg, rate, easSpaceHz) {
			dec[p] = 1
		}
	}
	// SAME always begins each header with sixteen 0xAB bytes, whose 8N1 bit
	// pattern is periodic and lets the receiver find the byte boundary even
	// though the bytes themselves carry no sync marker.
	start := easAlign(dec, spb)
	if start < 0 {
		return nil
	}
	bytes := easBytes(samples, rate, spb, win, start)
	return parseEAS(bytes)
}

// easPreambleBits is the 8N1 bit pattern of one 0xAB byte: a space start
// bit, eight data bits least-significant first (0xAB = 10101011), and a
// mark stop bit - ten bit periods in all.
var easPreambleBits = []byte{0, 1, 1, 0, 1, 0, 1, 0, 1, 1}

// easAlign finds the sample offset of the first 0xAB preamble byte by
// scoring the decision stream against the repeating preamble pattern. It
// returns -1 when no run close enough to the pattern is found.
func easAlign(dec []byte, spb float64) int {
	const patternBytes = 10
	perByte := len(easPreambleBits) // 10 bits: start + 8 data + stop
	need := int(float64(perByte*patternBytes) * spb)
	if len(dec) < need {
		return -1
	}
	best, bestScore := -1, 0.0
	for start := 0; start+need < len(dec); start++ {
		if dec[start] != 0 { // a preamble byte always starts with a space
			continue
		}
		score, total := 0, 0
		for b := 0; b < patternBytes; b++ {
			for k, want := range easPreambleBits {
				centre := start + int((float64(b*perByte+k)+0.5)*spb)
				if centre >= len(dec) {
					break
				}
				total++
				if dec[centre] == want {
					score++
				}
			}
		}
		if total > 0 {
			s := float64(score) / float64(total)
			if s > bestScore {
				best, bestScore = start, s
			}
		}
	}
	if bestScore < 0.9 {
		return -1
	}
	return best
}

// easBytes reads asynchronous 8-bit bytes on a fixed byte grid anchored at
// start (found from the preamble). Each byte is one start bit (space), eight
// data bits LSB first and one stop bit (mark), each measured at its centre.
func easBytes(samples []float64, rate int, spb float64, win, start int) []byte {
	bitAt := func(byteStart float64, bit int) byte {
		centre := byteStart + (float64(bit)+0.5)*spb
		from := int(centre) - win/2
		if from < 0 {
			from = 0
		}
		to := from + win
		if to > len(samples) {
			return 0
		}
		seg := samples[from:to]
		if goertzel(seg, rate, easMarkHz) >= goertzel(seg, rate, easSpaceHz) {
			return 1
		}
		return 0
	}
	var bytes []byte
	byteStart := float64(start)
	// Allow the final byte even if its stop bit lands in the last samples.
	for byteStart+9*spb < float64(len(samples)) {
		// Resync if the grid slipped off a start bit.
		if bitAt(byteStart, 0) != 0 {
			byteStart++
			continue
		}
		var b byte
		for d := 0; d < 8; d++ {
			if bitAt(byteStart, d+1) == 1 {
				b |= 1 << d
			}
		}
		if bitAt(byteStart, 9) != 1 { // stop bit must be mark
			byteStart++
			continue
		}
		bytes = append(bytes, b)
		byteStart += 10 * spb
	}
	return bytes
}

// parseEAS scans a decoded byte stream for SAME codes. It looks for the
// ASCII markers ZCZC (header) and NNNN (end of message) and collects the
// printable run that follows, matching the FCC message layout.
func parseEAS(data []byte) []EASAlert {
	var out []EASAlert
	text := string(data)
	for {
		z := strings.Index(text, "ZCZC")
		n := strings.Index(text, "NNNN")
		switch {
		case z >= 0 && (n < 0 || z < n):
			raw := takeEASRun(text[z:])
			out = append(out, parseEASHeader(raw))
			text = text[z+len(raw):]
		case n >= 0:
			out = append(out, EASAlert{Raw: "NNNN", End: true})
			text = text[n+4:]
		default:
			return out
		}
	}
}

// takeEASRun returns the leading run of characters that belong to one SAME
// code: ASCII letters, digits, and the - + / separators, ending at the first
// other byte.
func takeEASRun(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '+' || c == '/' {
			b.WriteByte(c)
			continue
		}
		break
	}
	return b.String()
}

// parseEASHeader splits "ZCZC-ORG-EEE-PSSCCC-PSSCCC+TTTT-JJJHHMM-LLLLLLLL-"
// into its fields. There may be one or more PSSCCC location codes separated
// by hyphens; the last one carries the "+TTTT" duration.
func parseEASHeader(raw string) EASAlert {
	a := EASAlert{Raw: raw}
	parts := strings.Split(raw, "-")
	if len(parts) < 4 {
		return a
	}
	a.Origin = parts[1]
	a.Event = parts[2]
	// Everything after EEE is a location code until one carries "+TTTT".
	i := 3
	for ; i < len(parts); i++ {
		loc := parts[i]
		if j := strings.Index(loc, "+"); j >= 0 {
			a.Locations = append(a.Locations, loc[:j])
			a.Duration = loc[j+1:]
			i++
			break
		}
		if loc != "" {
			a.Locations = append(a.Locations, loc)
		}
	}
	if i < len(parts) {
		a.Timestamp = parts[i]
		i++
	}
	if i < len(parts) {
		a.Station = parts[i]
	}
	return a
}
