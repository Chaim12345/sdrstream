package decode

import (
	"math"
	"strings"
)

// NAVTEX is the maritime navigational-telex service. It layers its messages
// on SITOR collective B-mode (AMTOR-FEC): 100 baud FSK with a 170 Hz shift
// carrying the constant-weight CCIR 476 code, in which every character has
// exactly four mark bits and three space bits so a single flipped bit is
// detectable. Every character is sent twice, the second copy 35 bits (five
// characters) later, which gives forward error correction.
//
// A message is "ZCZC B1B2B3B4" followed by free text and the terminator
// "NNNN"; B1 is the transmitter identity, B2 the subject indicator and B3B4
// the serial number.
const (
	navtexBaud   = 100.0
	navtexShift  = 85.0 // half the 170 Hz shift
	navtexCenter = 1700.0

	ccirLTRS  = 0x5A
	ccirFIGS  = 0x36
	ccirREP   = 0x66
	ccirAlpha = 0x0F
	ccirBeta  = 0x33
	ccirSpace = 0x5C
	ccirCR    = 0x78
	ccirLF    = 0x6C
)

// ccirLetters maps a CCIR 476 code to its letters-mode character. Zero means
// the code is a control symbol or has no letters-mode meaning.
var ccirLetters = map[byte]byte{
	0x47: 'A', 0x72: 'B', 0x1D: 'C', 0x53: 'D', 0x56: 'E', 0x1B: 'F',
	0x35: 'G', 0x69: 'H', 0x4D: 'I', 0x17: 'J', 0x1E: 'K', 0x65: 'L',
	0x39: 'M', 0x59: 'N', 0x71: 'O', 0x2D: 'P', 0x2E: 'Q', 0x55: 'R',
	0x4B: 'S', 0x74: 'T', 0x4E: 'U', 0x3C: 'V', 0x27: 'W', 0x3A: 'X',
	0x2B: 'Y', 0x63: 'Z',
}

// ccirFigures maps a CCIR 476 code to its figures-mode character.
var ccirFigures = map[byte]byte{
	0x2D: '0', 0x2E: '1', 0x27: '2', 0x56: '3', 0x55: '4', 0x74: '5',
	0x2B: '6', 0x4E: '7', 0x4D: '8', 0x71: '9', 0x17: '\'', 0x1B: '!',
	0x1D: ':', 0x1E: '(', 0x35: '&', 0x39: '.', 0x3A: '/', 0x3C: '=',
	0x47: '-', 0x53: '$', 0x59: ',', 0x63: '+', 0x65: ')', 0x69: '#',
	0x72: '?',
}

// NAVTEXMessage is one decoded NAVTEX broadcast.
type NAVTEXMessage struct {
	Station string // transmitter identity character (B1)
	Subject string // subject indicator character (B2)
	Serial  string // two-character serial number (B3B4)
	Text    string // message body between the header and NNNN
	Raw     string // the whole decoded text
}

// NAVTEXDecode demodulates SITOR-B audio and returns the NAVTEX messages it
// contains. centerHz is the audio centre of the FSK (mark and space are
// centre +/- 85 Hz); zero means the usual 1700 Hz.
func NAVTEXDecode(samples []float64, rate int, centerHz float64) []NAVTEXMessage {
	if rate < 4000 || len(samples) < rate/2 {
		return nil
	}
	if centerHz == 0 {
		centerHz = navtexCenterHz(samples, rate)
	}
	mark := centerHz + navtexShift
	space := centerHz - navtexShift
	spb := float64(rate) / navtexBaud
	win := int(spb * 0.8)
	if win < 8 {
		win = 8
	}
	half := win / 2
	n := len(samples)
	dec := make([]byte, n)
	for p := half; p+half < n; p++ {
		seg := samples[p-half : p+half]
		if goertzel(seg, rate, mark) >= goertzel(seg, rate, space) {
			dec[p] = 1
		}
	}
	// Clock drift between transmitter and receiver makes one global bit grid
	// wrong late in a long capture, so each 45 s chunk is aligned on its own.
	chunk := navtexChunkSeconds * rate
	var text strings.Builder
	for lo := 0; lo < n; lo += chunk {
		hi := lo + chunk
		if hi > n {
			hi = n
		}
		if hi-lo < rate/2 || navtexQuiet(samples[lo:hi]) {
			continue
		}
		text.WriteString(navtexChunkText(dec[lo:hi], float64(rate)))
	}
	return navtexMessages(text.String())
}

const navtexChunkSeconds = 45

// navtexQuiet reports whether a chunk carries no signal worth decoding, using
// its RMS against a floor well under any real FSK tone.
func navtexQuiet(seg []float64) bool {
	var sum float64
	for _, v := range seg {
		sum += v * v
	}
	return len(seg) == 0 || math.Sqrt(sum/float64(len(seg))) < 0.005
}

// navtexChunkText searches start phase, polarity and a narrow symbol-clock
// range for one chunk and returns its text.
func navtexChunkText(dec []byte, rate float64) string {
	var bestChars []byte
	bestScore := -1.0
	for baud := 99.0; baud <= 101.001; baud += 0.1 {
		spb := rate / baud
		step := spb / 14
		for pol := 0; pol < 2; pol++ {
			for start := 0.0; start < 7*spb; start += step {
				chars := navtexChars(dec, start, spb, pol == 1)
				if score := navtexScore(chars); score > bestScore {
					bestScore, bestChars = score, chars
				}
			}
		}
	}
	if bestScore < 0.5 {
		return ""
	}
	// SITOR-B alternates DX and RX slots. A DX character in slot 2j is
	// repeated in RX slot 2j+5, so only one slot parity carries the first
	// copies. Try both and keep the one that yields the most framing.
	a := navtexText(navtexMerge(bestChars, 0))
	b := navtexText(navtexMerge(bestChars, 1))
	if navtexRank(b) > navtexRank(a) {
		return b
	}
	return a
}

func navtexRank(t string) int {
	return 1000*strings.Count(t, "ZCZC") + strings.Count(t, "NNNN")*100 + len(t)
}

// navtexMerge returns the DX stream for one slot parity, replacing any
// character that fails the constant-weight check with its RX repeat.
func navtexMerge(chars []byte, phase int) []byte {
	var out []byte
	for j := 0; phase+2*j < len(chars); j++ {
		dx := chars[phase+2*j]
		rxIdx := phase + 2*(j+2) + 1
		if popcount7(dx) == 4 {
			out = append(out, dx)
		} else if rxIdx < len(chars) && popcount7(chars[rxIdx]) == 4 {
			out = append(out, chars[rxIdx])
		} else {
			out = append(out, 0)
		}
	}
	return out
}

// navtexCenterHz estimates the center of the 170 Hz FSK pair by searching
// for the candidate pair whose weaker tone has the most energy. Scoring the
// weaker tone rejects a single unrelated carrier that would dominate a
// one-tone peak detector.
func navtexCenterHz(samples []float64, rate int) float64 {
	bestCenter, bestScore := navtexCenter, -1.0
	// NAVTEX audio commonly lies near 1700 Hz but receivers can place it
	// anywhere in the usable SSB passband.
	if len(samples) > rate/2 {
		samples = samples[:rate/2]
	}
	for center := 900.0; center <= 2800; center += 5 {
		mark := goertzel(samples, rate, center+navtexShift)
		space := goertzel(samples, rate, center-navtexShift)
		score := math.Min(mark, space)
		if score > bestScore {
			bestScore, bestCenter = score, center
		}
	}
	return bestCenter
}

// navtexChars samples the decision stream on the bit grid anchored at start
// and groups the bits into 7-bit CCIR 476 characters. invert flips the
// polarity, which lets a mark/space swap still decode.
func navtexChars(dec []byte, start, spb float64, invert bool) []byte {
	var chars []byte
	var code byte
	bit := 0
	for pos := start; ; pos += spb {
		idx := int(pos + spb/2)
		if idx < 0 || idx >= len(dec) {
			break
		}
		v := dec[idx]
		if invert {
			v ^= 1
		}
		code |= v << uint(bit) // CCIR 476 is sent least-significant bit first
		bit++
		if bit == 7 {
			chars = append(chars, code)
			code = 0
			bit = 0
		}
	}
	return chars
}

// navtexScore is the fraction of characters that are valid CCIR 476 symbols
// (exactly four mark bits), used to pick the bit alignment.
func navtexScore(chars []byte) float64 {
	if len(chars) < 10 {
		return -1
	}
	valid := 0
	for _, c := range chars {
		if popcount7(c) == 4 {
			valid++
		}
	}
	return float64(valid) / float64(len(chars))
}

// popcount7 counts the set bits in the low seven bits of a CCIR 476 code.
func popcount7(c byte) int {
	n := 0
	for i := 0; i < 7; i++ {
		n += int(c>>uint(i)) & 1
	}
	return n
}

// navtexText decodes a merged DX character stream into text, following the
// letters/figures shifts.
func navtexText(chars []byte) string {
	var out strings.Builder
	figures := false
	for i := 0; i < len(chars); i++ {
		c := chars[i]
		switch c {
		case ccirLTRS:
			figures = false
		case ccirFIGS:
			figures = true
		case ccirREP, ccirAlpha, ccirBeta:
			// idle / phasing symbols carry no text
		case ccirSpace:
			out.WriteByte(' ')
		case ccirCR:
			out.WriteByte('\n')
		case ccirLF:
			// CRLF is sent as a pair; keep one line break
		default:
			if ch, ok := ccirLetters[c]; ok && !figures {
				out.WriteByte(ch)
			} else if ch, ok := ccirFigures[c]; ok && figures {
				out.WriteByte(ch)
			}
		}
	}
	return out.String()
}

// navtexMessages splits decoded text into messages delimited by ZCZC and
// NNNN. The four characters after ZCZC are the message header.
func navtexMessages(text string) []NAVTEXMessage {
	var out []NAVTEXMessage
	rest := text
	for {
		z := strings.Index(rest, "ZCZC")
		if z < 0 {
			break
		}
		rest = rest[z+4:]
		// A space or CR usually separates ZCZC from the header.
		rest = strings.TrimLeft(rest, " ")
		if len(rest) < 4 {
			break
		}
		hdr := rest[:4]
		rest = rest[4:]
		end := strings.Index(rest, "NNNN")
		body := rest
		if end >= 0 {
			body = rest[:end]
			rest = rest[end+4:]
		} else {
			rest = ""
		}
		out = append(out, NAVTEXMessage{
			Station: string(hdr[0]),
			Subject: string(hdr[1]),
			Serial:  hdr[2:4],
			Text:    strings.TrimSpace(body),
			Raw:     "ZCZC " + hdr + body,
		})
	}
	return out
}
