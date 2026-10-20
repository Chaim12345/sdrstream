package decode

import "strings"

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
		centerHz = navtexCenter
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
	// Search one character period (7 bits) for the start offset and try both
	// mark/space polarities, keeping the alignment that yields the most valid
	// CCIR 476 characters. Only the first second is scored so a long capture
	// is not re-scanned for every candidate offset.
	scoreLen := int(float64(rate))
	if scoreLen > n {
		scoreLen = n
	}
	var bestStart float64
	bestInvert := false
	bestScore := -1.0
	step := spb / 14
	for pol := 0; pol < 2; pol++ {
		for start := 0.0; start < 7*spb; start += step {
			chars := navtexChars(dec[:scoreLen], start, spb, pol == 1)
			score := navtexScore(chars)
			if score > bestScore {
				bestScore = score
				bestStart = start
				bestInvert = pol == 1
			}
		}
	}
	if bestScore < 0.5 {
		return nil
	}
	bestChars := navtexChars(dec, bestStart, spb, bestInvert)
	// Every character is sent twice, alternating alpha and its repeat five
	// characters later, so only one parity carries the data.
	a := navtexText(bestChars, 0)
	b := navtexText(bestChars, 1)
	text := a
	if strings.Count(b, "ZCZC") > strings.Count(a, "ZCZC") {
		text = b
	}
	return navtexMessages(text)
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
		code = (code << 1) | v
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

// navtexText decodes the characters at one parity (every other character,
// starting at phase) into text, following the letters/figures shifts.
func navtexText(chars []byte, phase int) string {
	var out strings.Builder
	figures := false
	for i := phase; i < len(chars); i += 2 {
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
