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

// SSTVReport carries the decode evidence for one SSTV frame. VIS and
// parity prove a header was heard; they say nothing about frame
// geometry. Geometry evidence is the sync-lock count: a coasted line
// kept its expected position because no 1200 Hz sync pulse was found,
// so its pixels sit wherever the constant-clock assumption puts them.
// The type lives here so the publication gate can consume the actual
// report, not caller-extracted counts; decode aliases it.
type SSTVReport struct {
	VIS          int    // VIS code that selected the mode; never 0 from a real detect
	Mode         string // mode name, e.g. "Martin M1"
	Width        int
	Height       int
	DecodedLines int // lines whose channels sampled inside the audio
	CoastedLines int // lines with no sync lock; geometry unverified
}

// CoastFraction is the share of lines whose geometry rests on the
// constant-clock assumption rather than a locked sync pulse.
func (r SSTVReport) CoastFraction() float64 {
	if r.Height == 0 {
		return 0
	}
	return float64(r.CoastedLines) / float64(r.Height)
}

// sstvMaxCoastFraction bounds the share of frame lines whose sync
// pulse was never locked (the decoder coasted them on a constant-clock
// assumption). Provisional fixture-derived decision, not a physical
// fact: measured 2026-10-23, the good corpus (6 modes across the
// Martin, Scottie and Robot 36 families, clean and ~15 dB SNR
// impaired) locked every sync (0.000) while the constructed
// sync-loss + clock-drift negative coasted 0.316 with a torn render.
// 0.05 tolerates isolated fade losses while flagging systematic loss.
const sstvMaxCoastFraction = 0.05

// SSTVImage gates a decoded SSTV frame on the decoder's own report,
// not on PNG variance alone and not on caller-extracted counts: the
// VIS claim in a passing reason comes from the report itself. A
// parity-valid VIS header proves a header was heard; it says nothing
// about frame geometry. The only geometry evidence is the per-line
// sync-lock count, so the report is mandatory and must be internally
// consistent: a nil report, a zero VIS, impossible counts, or
// dimensions that do not match the decoded image all fail. Passing
// means coasting is bounded by the fixture-derived ceiling - bounded
// evidence, never certified geometry. Failing or absent evidence
// means the geometry is unverified, not necessarily bad.
func SSTVImage(kind string, data []byte, rep *SSTVReport) Result {
	if v := Image(kind, data); !v.Live {
		return v
	}
	if rep == nil {
		return Result{false, "sstv geometry unverified: no decode report (a VIS header is not geometry evidence)"}
	}
	if rep.VIS == 0 || rep.Mode == "" {
		return Result{false, "sstv geometry unverified: report has no source VIS/mode (a real detect always names both)"}
	}
	if rep.Width <= 0 || rep.Height <= 0 {
		return Result{false, fmt.Sprintf("sstv geometry unverified: report dimensions %dx%d impossible", rep.Width, rep.Height)}
	}
	if rep.CoastedLines < 0 || rep.CoastedLines > rep.Height {
		return Result{false, fmt.Sprintf("sstv geometry unverified: report coasted count %d of %d impossible", rep.CoastedLines, rep.Height)}
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return Result{false, "invalid PNG"}
	}
	if cfg.Width != rep.Width || cfg.Height != rep.Height {
		return Result{false, fmt.Sprintf("sstv geometry unverified: report %s %dx%d does not match decoded image %dx%d",
			rep.Mode, rep.Width, rep.Height, cfg.Width, cfg.Height)}
	}
	coast := rep.CoastFraction()
	if coast > sstvMaxCoastFraction {
		return Result{false, fmt.Sprintf("sstv geometry unverified: %d/%d lines coasted without sync lock (ceiling %.2f, fixture-derived)",
			rep.CoastedLines, rep.Height, sstvMaxCoastFraction)}
	}
	return Result{true, fmt.Sprintf("sstv reception verified: %s VIS 0x%02X (parity-checked); sync coasting bounded %d/%d - evidence, not geometry certification",
		rep.Mode, rep.VIS, rep.CoastedLines, rep.Height)}
}

// WEFAXImage gates a decoded WEFAX chart on the decoder's own sync
// evidence, not on PNG variance alone: a confidently wrong sync (false
// phasing wedge, biased period fit) still renders a large, high-variance
// PNG. The structural Image checks run first and are unchanged.
//
// The verdict separates two evidence tiers and Live requires both:
// reception (a sync was detected: wedge contrast / period fit) and
// geometry quality (the clock tracker's alignment is trustworthy).
// A decoded chart that fails geometry is NOT published as verified,
// however confident the sync. The floors are decision thresholds
// derived from fixture measurements on 2026-10-23, not physical
// constants: valid phasing-wedge fixtures refined to wedge contrast
// >= 0.86 while a false wedge measured 0.28; a good mid-chart
// line-correlation fit scored period 0.37 / deskew 0.38 while a
// biased fit scored 0.31 / 0.29; the drift sanity bound is three
// times the largest fixture drift. When evidence is ambiguous the
// verdict is not live.
func WEFAXImage(kind string, data []byte, syncSource string, wedgeContrast, driftPPM, driftMinPPM, driftMaxPPM, periodScore, deskewCorr float64) Result {
	if r := Image(kind, data); !r.Live {
		return r
	}
	wanderSpan := driftMaxPPM - driftMinPPM
	reception := fmt.Sprintf("reception: %s sync, wedge contrast %.2f, drift %+.0f ppm", syncSource, wedgeContrast, driftPPM)
	geometry := fmt.Sprintf("geometry: wander span %.0f ppm, deskew %.2f, period score %.2f", wanderSpan, deskewCorr, periodScore)
	const wedgeContrastMin = 0.5
	const periodScoreMin = 0.35
	const deskewMin = 0.30
	const driftAbsMax = 2000
	// Geometry-quality tier: plateau or adversarial content makes the
	// clock tracker's local votes erratic, so the per-pair wander range
	// blows out (fixture measurements 2026-10-23: valid decodes span
	// 11-788 ppm, known-misaligned renders 2961-3280 ppm; the floor
	// sits between, not a physical constant). A real phasing wedge
	// alone does NOT establish geometry: a tracker that lost alignment
	// behind a real wedge still renders garbage with a confident sync.
	const wanderSpanMax = 1500
	if driftPPM > driftAbsMax || driftPPM < -driftAbsMax {
		return Result{false, "wefax not verified: fitted drift beyond the sanity bound; " + reception + "; " + geometry}
	}
	if wanderSpan > wanderSpanMax {
		return Result{false, "wefax not verified: clock-tracker wander too wide to trust alignment; " + reception + "; " + geometry}
	}
	switch syncSource {
	case "phasing-wedge":
		if wedgeContrast < wedgeContrastMin {
			return Result{false, "wefax not verified: refined wedge contrast below floor; " + reception + "; " + geometry}
		}
	case "line-correlation":
		if periodScore < periodScoreMin || deskewCorr < deskewMin {
			return Result{false, "wefax not verified: mid-chart period fit below floor; " + reception + "; " + geometry}
		}
	default:
		return Result{false, "wefax not verified: unknown sync evidence; " + reception + "; " + geometry}
	}
	// Live attests reception/sync detection AND the geometry-quality
	// evidence above, plus PNG structure. It does not certify chart
	// legibility (the deskew metric is content-dependent) and the
	// floors are scoped to the fixture evidence cited, re-derivable
	// against real captures.
	return Result{true, "wefax verified: " + reception + "; " + geometry}
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
		if modeValue == "sstv" {
			// No generic bypass: an SSTV frame cannot be verified
			// from PNG bytes alone. The publication caller must run
			// SSTVImage with the decode report's sync-lock evidence.
			return errors.New("sstv images require the decode-report gate (SSTVImage); PNG checks are not geometry evidence")
		}
		if modeValue != "wefax" {
			return errors.New("image mode is not SSTV/WEFAX")
		}
		// WEFAX charts are gated by the decoder's own sync-evidence
		// verification (WEFAXImage, via the hfrun decode path) before
		// they reach this gate; here we only require a real PNG, same
		// as APT above.
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
