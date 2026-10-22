// Package verify is the sole quality gate for content that enters the live gallery.
package verify

import (
	"bytes"
	"errors"
	"fmt"
	"image/png"
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
	if len(text) < 12 {
		return Result{false, "too short"}
	}
	printable, letters := 0, 0
	for _, r := range text {
		if unicode.IsPrint(r) || r == '\n' || r == '\r' || r == '\t' {
			printable++
		}
		if unicode.IsLetter(r) {
			letters++
		}
	}
	if float64(printable)/float64(len(text)) < 0.9 || float64(letters)/float64(max(1, printable)) < 0.25 {
		return Result{false, "low text quality"}
	}
	if anchor != "" && strings.Contains(strings.ToUpper(text), strings.ToUpper(anchor)) {
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
			if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-') {
				return false
			}
		}
	}
	return true
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
