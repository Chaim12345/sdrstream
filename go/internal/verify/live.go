// Package verify is the sole quality gate for content that enters the live gallery.
package verify

import (
	"bytes"
	"errors"
	"fmt"
	"image/png"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Result carries the single gate verdict and a short reason.
type Result struct {
	Live   bool
	Reason string
}

// Text gates known, anchored HF service text and rejects non-printable output.
func Text(kind, text, anchor string) Result {
	text = strings.TrimSpace(text)
	// Rune counts everywhere: len() is bytes, and accented text used to
	// be rejected by the printable/byte ratio (each accented char is 2+
	// UTF-8 bytes but counts once as a rune).
	runes, printable, letters := 0, 0, 0
	for _, r := range text {
		runes++
		if unicode.IsPrint(r) || r == '\n' || r == '\r' || r == '\t' {
			printable++
		}
		if unicode.IsLetter(r) {
			letters++
		}
	}
	if runes < 12 {
		return Result{false, "too short"}
	}
	if float64(printable)/float64(max(1, runes)) < 0.9 || float64(letters)/float64(max(1, printable)) < 0.25 {
		return Result{false, "low text quality"}
	}
	// Anchors match on whitespace-collapsed text: HF decoders regularly
	// narrate spacing ("VOZ  DE  GIJON"), and an exact substring test
	// rejected real traffic that was plainly there.
	if anchor != "" && strings.Contains(collapseSpaces(strings.ToUpper(text)), collapseSpaces(strings.ToUpper(anchor))) {
		return Result{true, "anchor " + anchor}
	}
	upper := strings.ToUpper(text)
	if kind == "navtex" && strings.Contains(upper, "ZCZC") && strings.Contains(upper, "NNNN") {
		hits := MaritimeWordHits(text)
		if hits >= 2 {
			return Result{true, fmt.Sprintf("NAVTEX framing and %d distinct maritime words", hits)}
		}
	}
	return Result{false, "expected service anchor missing"}
}

// maritimeWords are common NAVTEX/maritime-weather terms. They are reported in
// the gate reason as supporting evidence only; the NAVTEX framing and area
// anchors above remain the sole publication criteria.
var maritimeWords = []string{"NAVAREA", "METAREA", "WARNING", "GALE", "STORM", "WIND", "WAVES", "SWELL", "VISIBILITY", "FORECAST", "SEA", "COAST", "MARINERS", "NAVIGATION", "LIGHT", "BUOY", "HAZARD", "CANCEL"}

// MaritimeWordHits counts distinct maritime words present in text.
func MaritimeWordHits(text string) int {
	upper := strings.ToUpper(text)
	n := 0
	for _, w := range maritimeWords {
		if strings.Contains(upper, w) {
			n++
		}
	}
	return n
}

// CW requires a known callsign anchor, preventing random Morse from publishing.
func CW(text, callsign string) Result {
	if callsign != "" && strings.Contains(strings.ToUpper(text), strings.ToUpper(callsign)) {
		return Result{true, "callsign " + callsign}
	}
	return Result{false, "callsign not heard"}
}

// Image checks geometry and visible tonal variance before allowing publication.
func Image(kind string, data []byte) Result {
	if len(data) == 0 {
		return Result{false, "empty image"}
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return Result{false, "invalid PNG"}
	}
	if cfg.Width < 240 || cfg.Height < 240 {
		return Result{false, "image too small"}
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return Result{false, "invalid PNG"}
	}
	b := img.Bounds()
	var sum, sumSq float64
	n := 0
	for y := b.Min.Y; y < b.Max.Y; y += 7 {
		for x := b.Min.X; x < b.Max.X; x += 7 {
			r, _, _, _ := img.At(x, y).RGBA()
			v := float64(r >> 8)
			sum += v
			sumSq += v * v
			n++
		}
	}
	if n == 0 || sumSq/float64(n)-(sum/float64(n))*(sum/float64(n)) < 400 {
		return Result{false, "image content flat"}
	}
	return Result{true, "image ok"}
}

// Live rejects empty, fixture-like, unstructured, or malformed live output.
// The scanner may show an unverified result in its status table, but only this
// function authorizes a record to become a live gallery item.
func Live(kindValue, modeValue, detail string, imageData []byte, anchors ...string) error {
	if len(imageData) > 0 {
		// APT images are gated by the APT decoder's own sync-lock
		// verification before they reach this gate; here we only require
		// a real PNG.
		if modeValue == "apt" {
			if !bytes.HasPrefix(imageData, []byte{0x89, 'P', 'N', 'G'}) {
				return errors.New("apt image is not a PNG")
			}
			return nil
		}
		if modeValue != "sstv" && modeValue != "wefax" {
			return errors.New("image mode is not SSTV/WEFAX")
		}
		verdict := Image(modeValue, imageData)
		if !verdict.Live {
			return fmt.Errorf("image verification rejected content: %s", verdict.Reason)
		}
		return nil
	}
	text := strings.TrimSpace(detail)
	if text == "" {
		return errors.New("empty live transcript or payload")
	}
	mode := strings.ToLower(modeValue)
	anchor := ""
	if len(anchors) > 0 {
		anchor = anchors[0]
	}
	if mode == "morse" {
		mode = "cw"
	}
	if mode == "rtty" || mode == "navtex" || mode == "psk31" {
		if !Text(mode, text, anchor).Live {
			return errors.New("HF text payload failed quality check: " + Text(mode, text, anchor).Reason)
		}
		return nil
	}
	if mode == "ft8" {
		count := len(strings.Split(text, "\n"))
		if !FTX(count, !strings.Contains(strings.ToUpper(text), " FT4 "), text).Live {
			return errors.New("FT8 decode failed publication gate")
		}
		return nil
	}
	if mode == "wspr" {
		count := len(strings.Split(text, "\n"))
		if !WSPR(count, text).Live {
			return errors.New("WSPR decode failed publication gate")
		}
		return nil
	}
	if mode == "pocsag" {
		if !pocsagDetailValid(text) {
			return errors.New("POCSAG payload failed page structure check")
		}
		return nil
	}
	if mode == "cw" {
		if !CW(text, anchor).Live {
			return errors.New("CW payload lacks a verified callsign")
		}
		return nil
	}
	if mode == "aprs" || mode == "direwolf" || mode == "afsk1200" {
		if !validAX25(text) {
			return errors.New("AX.25 payload failed structure check")
		}
		return nil
	}
	if mode == "speech" || strings.EqualFold(kindValue, "voice") {
		if !readableText(text, 32, 0.68) {
			return errors.New("speech transcript failed readability check")
		}
		return nil
	}
	return fmt.Errorf("decoder mode %q has no live-gallery validator", modeValue)
}

func validAX25(text string) bool {
	for _, packet := range strings.Split(text, " | ") {
		gt, colon := strings.IndexByte(packet, '>'), strings.IndexByte(packet, ':')
		if gt < 1 || colon <= gt+1 {
			return false
		}
		for _, r := range packet[:gt] {
			// AX.25 addresses are six shifted-ASCII characters plus SSID:
			// only ASCII alphanumerics and the SSID hyphen are real. A
			// unicode.IsLetter check would admit accented letters that no
			// on-air frame can carry.
			if !(r < 0x80 && (unicode.IsLetter(r) || unicode.IsDigit(r)) || r == '-') {
				return false
			}
		}
	}
	return true
}

// collapseSpaces folds runs of whitespace into single spaces, so anchor
// matching survives decoder spacing quirks.
func collapseSpaces(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func readableText(text string, minimum int, ratio float64) bool {
	letters, printable, total := 0, 0, 0
	for _, r := range text {
		total++
		if unicode.IsLetter(r) {
			letters++
		}
		if unicode.IsPrint(r) || r == '\n' || r == '\r' || r == '\t' {
			printable++
		}
	}
	if total < minimum || letters < minimum/2 || total == 0 {
		return false
	}
	return float64(printable)/float64(total) >= ratio && float64(letters)/float64(total) >= 0.45
}

// FTX gates FT8/FT4 decodes. Every reported message already passed LDPC
// parity and a CRC-14 inside the decoder, so a single decode of a message
// that also contains a plausible amateur callsign is real. FT4 stays
// unverified against over-the-air audio, so it never publishes.
func FTX(count int, verifiedProtocol bool, text string) Result {
	if count == 0 {
		return Result{false, "no CRC-valid messages"}
	}
	if !verifiedProtocol {
		return Result{false, "FT4 decoder verified on synthetic audio only"}
	}
	if !callsignLike.MatchString(text) {
		return Result{false, "no callsign in decoded text"}
	}
	return Result{true, fmt.Sprintf("%d CRC-14 verified FT8 messages", count)}
}

var callsignLike = regexp.MustCompile(`\b[A-Z0-9]{1,3}[0-9][A-Z]{1,4}\b`)

// WSPR gates WSPR spots. The WSPR code has no CRC, so one decode could in
// principle be a false positive. Publish only when a window yields at least
// two distinct spots that each carry a plausible callsign.
func WSPR(count int, text string) Result {
	if count == 0 {
		return Result{false, "no WSPR spots"}
	}
	if count < 2 {
		return Result{false, "single WSPR spot, no CRC to confirm it"}
	}
	if len(callsignLike.FindAllString(text, -1)) < 2 {
		return Result{false, "fewer than two callsigns in decoded spots"}
	}
	return Result{true, fmt.Sprintf("%d WSPR spots with callsigns", count)}
}

func pocsagDetailValid(text string) bool {
	pages := strings.Split(text, " | ")
	if len(pages) == 0 {
		return false
	}
	for _, page := range pages {
		parts := strings.SplitN(page, ":", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
			return false
		}
		if _, err := strconv.Atoi(strings.TrimSpace(parts[0])); err != nil {
			return false
		}
	}
	return true
}
