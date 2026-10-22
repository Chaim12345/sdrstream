// Package regress runs the project's decoders against public real-world
// recordings and compares the output with reference decodes (WSJT-X, the
// ft8_lib C decoder, multimon-ng, acarsdec). It exists to catch regressions
// and to list decoder gaps: every case reports how many reference messages
// the Go decoder reproduced, how many it missed and how many it invented.
//
// Recordings are not stored in the repository. They are fetched on demand
// from their public home (GitHub, SourceForge, signals.taunus.de), checked
// against a pinned SHA-256 and cached. The reference decodes live in the
// golden/ directory (embedded); scripts/regress_golden.sh regenerates them
// from the reference tools.
package regress

import (
	"embed"
	"fmt"
	"sort"
	"strings"
)

//go:embed golden/*.txt
var goldenFS embed.FS

// Status of one case.
const (
	Pass = "PASS" // every reference message decoded, nothing invented
	Gap  = "GAP"  // at or above the regression floor but below the reference
	Fail = "FAIL" // below the floor, false positives, or a decoder error
	Info = "INFO" // no reference available; counts only
	Skip = "SKIP" // recording unavailable (offline)
)

// Case is one recording plus the decoder to run on it.
type Case struct {
	Name    string // short unique id
	Decoder string // ft8, ft4, wspr, pocsag, flex, acars, hfdl, aprs
	File    string // cache file name (a .wav; .flac sources are converted)
	Source  string // human readable provenance
	URL     string // where File is fetched from
	SHA256  string // pinned digest of the downloaded file, "" = not pinned
	Ref     string // reference decoder that produced the golden list
	Golden  string // golden file name in golden/, "" = no reference
	// Floor is the number of reference messages the decoder must reproduce
	// today. Reaching Floor but not the full reference is a GAP, not a FAIL.
	Floor int
	// MaxExtra is the number of unconfirmed extra decodes tolerated today.
	MaxExtra int
	// MinFrames applies to cases without a message-level reference (hfdl, or
	// a decoder count only): the minimum number of decoded items.
	MinFrames int
	// Local, when set, is a path relative to the go/ module root used instead
	// of the cache (recordings that are committed to the repository).
	Local string
	Args  map[string]float64 // decoder arguments, for example carrier Hz
}

// Result of running one case.
type Result struct {
	Case        Case
	Expected    int      // reference messages (0 when there is no reference)
	Got         int      // distinct messages the decoder produced
	Matched     int      // reference messages reproduced
	Extra       []string // decoded but in neither the reference nor the accepted set
	Bonus       []string // decoded, not in the reference, but confirmed by a second decoder
	Unsupported []string // CRC-valid decodes whose message type the decoder cannot unpack
	Missed      []string // in the reference but not decoded
	Status      string
	Note        string
}

// Normalize makes decoder and reference text comparable: control characters
// and multimon-ng style <TAGS> are dropped, whitespace is collapsed.
func Normalize(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '<' {
			if j := strings.IndexByte(s[i:], '>'); j > 0 && j <= 5 && strings.ToUpper(s[i+1:i+j]) == s[i+1:i+j] && s[i+1:i+j] != "..." {
				i += j
				continue
			}
		}
		if c < 0x20 || c == 0x7f {
			c = ' '
		}
		b.WriteByte(c)
	}
	t := strings.Join(strings.Fields(b.String()), " ")
	return strings.ReplaceAll(strings.ReplaceAll(t, "| ", "|"), " |", "|")
}

// loadGolden returns the reference set for a case.
func loadGolden(name string) (map[string]bool, error) {
	b, err := goldenFS.ReadFile("golden/" + name)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			out[Normalize(l)] = true
		}
	}
	return out, nil
}

// compare fills the counts of r from the decoded set and reference.
func compare(r *Result, got, ref, ok map[string]bool) {
	r.Got = len(got)
	r.Expected = len(ref)
	if r.Case.Floor < 0 {
		r.Case.Floor = r.Expected
	}
	for k := range ref {
		if got[k] {
			r.Matched++
		} else {
			r.Missed = append(r.Missed, k)
		}
	}
	for k := range got {
		switch {
		case ref[k]:
		case ok[k]:
			r.Bonus = append(r.Bonus, k)
		default:
			r.Extra = append(r.Extra, k)
		}
	}
	sort.Strings(r.Bonus)
	sort.Strings(r.Missed)
	sort.Strings(r.Extra)
	switch {
	case len(r.Extra) > r.Case.MaxExtra || r.Matched < r.Case.Floor:
		r.Status = Fail
	case r.Matched < r.Expected || len(r.Extra) > 0:
		r.Status = Gap
	default:
		r.Status = Pass
	}
}

// Summary line used by the table footer.
func Summary(rs []Result) string {
	n := map[string]int{}
	for _, r := range rs {
		n[r.Status]++
	}
	return fmt.Sprintf("%d PASS, %d GAP, %d FAIL, %d INFO, %d SKIP", n[Pass], n[Gap], n[Fail], n[Info], n[Skip])
}
