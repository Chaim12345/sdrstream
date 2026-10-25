package hfschedule

// RTTY cross-verification. The Go RTTY decoder's known failure mode is
// readable-looking false text from a strong carrier, so a scheduled RTTY
// capture is only marked verified when an independent reference decoder
// (minimodem, see decode/rttyref.go) recovers agreeing text from the same
// audio. The gate verdict alone never sets Verified.

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/chaim12345/sdrstream/go/internal/decode"
)

// RTTYVerdict is the cross-check outcome for one capture. Checked reports
// whether the reference engine actually ran; a missing binary or engine
// failure leaves Verified false with the reason recorded.
type RTTYVerdict struct {
	Checked       bool
	Engine        string
	RefTextPath   string
	Verified      bool
	VerifiedLines int
	Reason        string
}

// rttyLineWidth wraps normalized decode text for the per-line agreement
// count. RTTY teleprinter lines are traditionally 40-69 characters; the
// wrap width only shapes the reported line metric, not the verdict.
const rttyLineWidth = 40

// rttyShingle is the character-shingle size for fuzzy line agreement:
// HF decoders garble individual characters independently, so two engines
// reading the same signal rarely produce byte-identical long lines.
const rttyShingle = 8

var rttySpaceRE = regexp.MustCompile(`\s+`)

// normalizeRTTY upper-cases text and collapses all whitespace runs to
// single spaces, so spacing narration never decides agreement.
func normalizeRTTY(s string) string {
	return rttySpaceRE.ReplaceAllString(strings.ToUpper(strings.TrimSpace(s)), " ")
}

// rttyWrap splits normalized text into width-character lines.
func rttyWrap(s string, width int) []string {
	var out []string
	for len(s) > width {
		out = append(out, s[:width])
		s = s[width:]
	}
	if s != "" {
		out = append(out, s)
	}
	return out
}

// shingleOverlap returns the fraction of line's shingles (8-char windows,
// stepped by 4) that appear in hay. A full-length line has enough shingles
// for the fraction to mean something; short lines must match verbatim.
func shingleOverlap(line, hay string) float64 {
	if len(line) <= rttyShingle {
		if strings.Contains(hay, line) {
			return 1
		}
		return 0
	}
	total, hits := 0, 0
	for i := 0; i+rttyShingle <= len(line); i += 4 {
		total++
		if strings.Contains(hay, line[i:i+rttyShingle]) {
			hits++
		}
	}
	if total == 0 {
		return 0
	}
	return float64(hits) / float64(total)
}

// RTTYAgreement compares the Go decode with the reference decode. Verified
// requires the reference to have produced non-trivial text AND either the
// expected service anchor in BOTH decoders, or at least one reference line
// whose shingles land at >=80% inside the Go text (false carrier text never
// produces a 40-character shingle match in an independent engine). The line
// count is the number of reference lines meeting that 80% test, the metric
// reported as verified lines.
func RTTYAgreement(goText, refText, anchor string) (verified bool, lines int, reason string) {
	g := normalizeRTTY(goText)
	r := normalizeRTTY(refText)
	if len(r) < 12 {
		return false, 0, "reference decode empty or trivial"
	}
	anchor = strings.ToUpper(strings.TrimSpace(anchor))
	anchorBoth := anchor != "" && strings.Contains(g, anchor) && strings.Contains(r, anchor)
	for _, line := range rttyWrap(r, rttyLineWidth) {
		if shingleOverlap(line, g) >= 0.8 {
			lines++
		}
	}
	switch {
	case anchorBoth && lines > 0:
		return true, lines, fmt.Sprintf("anchor %s in both engines, %d agreeing lines", anchor, lines)
	case anchorBoth:
		return true, 0, fmt.Sprintf("anchor %s in both engines", anchor)
	case lines >= 1:
		return true, lines, fmt.Sprintf("%d agreeing lines, anchor not in both engines", lines)
	}
	return false, 0, "engines disagree"
}

// CrossCheckRTTY runs the reference decoder on one saved RTTY capture and
// writes its output next to the Go decode as <key>.ref.txt. The WAV, the Go
// text (<key>.txt) and the reference text together are the audit trail.
// The context carries the overall capture deadline; the engine itself caps
// at two minutes.
func CrossCheckRTTY(ctx context.Context, t decode.HFTarget, wavPath, goText, dir, key string) RTTYVerdict {
	res, err := decode.RTTYRefDecode(ctx, "", wavPath, t.MarkHz, t.SpaceHz, t.Baud)
	if err != nil {
		return RTTYVerdict{Reason: "reference unavailable: " + err.Error()}
	}
	v := RTTYVerdict{Checked: true, Engine: res.Engine}
	refPath := dir + "/" + key + ".ref.txt"
	if err := atomicWrite(refPath, []byte(res.Text)); err != nil {
		v.Reason = "reference output not saved: " + err.Error()
		return v
	}
	v.RefTextPath = refPath
	v.Verified, v.VerifiedLines, v.Reason = RTTYAgreement(goText, res.Text, t.Expect)
	return v
}
