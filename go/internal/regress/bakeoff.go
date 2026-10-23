package regress

// The bake-off runs the external reference decoders (decode.RefDecoder)
// over the same corpus as Run and rates both against the golden decodes.
// Policy: keep the Go decoder for a family only where its recall comes
// within 2% of the reference tool's recall; otherwise prefer the reference.

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/decode"
)

// RefRun executes one case with one reference tool and compares its output
// with the golden list, exactly like Run does for the Go decoder.
func RefRun(ctx context.Context, c Case, dir, root, tool string) (res Result) {
	res = Result{Case: c}
	res.Case.Floor = -1 // strict: rate the tool against the full reference
	res.Case.MaxExtra = 1 << 30
	path := filepath.Join(dir, c.File)
	if c.Local != "" {
		path = filepath.Join(root, c.Local)
	}
	var dec decode.RefDecoder
	var mode string
	switch tool {
	case "jt9":
		dec = decode.JT9("")
		mode = c.Decoder
	case "ft8lib":
		dec = decode.FT8Lib("")
		mode = c.Decoder
	case "wsprd":
		dec = decode.WSPRD("")
	case "multimon-ng":
		dec = decode.Multimon("")
		switch c.Decoder {
		case "pocsag":
			mode = "POCSAG512,POCSAG1200,POCSAG2400"
		case "flex":
			mode = "FLEX"
		case "aprs":
			mode = "AFSK1200"
		default:
			res.Status, res.Note = Skip, "no multimon-ng mapping for "+c.Decoder
			return res
		}
	case "acarsdec":
		dec = decode.Acarsdec("")
	default:
		res.Status, res.Note = Skip, "unknown tool "+tool
		return res
	}
	if dec.Binary() == "" {
		res.Status, res.Note = Skip, tool+" not installed"
		return res
	}
	// acarsdec needs its fixed 12500 Hz input rate. Files already at that
	// rate (possibly multi-channel, one channel per frequency) decode
	// directly; anything else is resampled to mono 12.5 kHz.
	runPath := path
	if tool == "acarsdec" {
		if _, rate, err := decode.ReadWAV(path); err != nil {
			res.Status, res.Note = Fail, err.Error()
			return res
		} else if rate != 12500 {
			s, _, _ := decode.ReadWAV(path)
			if runPath, err = decode.AcarsdecWAV(s, rate, ""); err != nil {
				res.Status, res.Note = Fail, err.Error()
				return res
			}
		}
	}
	rctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	refs, err := dec.DecodeWAV(rctx, runPath, mode)
	if err != nil {
		res.Status, res.Note = Fail, err.Error()
		return res
	}
	got := map[string]bool{}
	for _, m := range refs {
		got[Normalize(m.Text)] = true
	}
	if c.Golden == "" {
		res.Got = len(got)
		res.Status = Info
		return res
	}
	ref, err := loadGolden(c.Golden)
	if err != nil {
		res.Status, res.Note = Fail, err.Error()
		return res
	}
	compare(&res, got, ref, nil)
	res.Status = "" // bake-off rows carry no pass/fail, only counts
	return res
}

// BakeoffRow is one case x tool cell of the bake-off table.
type BakeoffRow struct {
	Case     string
	Family   string
	Tool     string // "go" or the reference tool name
	Expected int
	Got      int
	Matched  int
	Extra    int
	Status   string // Go side keeps PASS/GAP/FAIL; ref side is informational
	Note     string
}

// refToolsFor lists the reference tools that cover a case's family.
func refToolsFor(family string) []string {
	switch family {
	case "ft8", "ft4":
		return []string{"jt9", "ft8lib"}
	case "wspr":
		return []string{"wsprd"}
	case "pocsag", "flex", "aprs":
		return []string{"multimon-ng"}
	case "acars":
		return []string{"acarsdec"}
	}
	return nil
}

// Bakeoff runs the Go decoder and every covering reference tool on each
// available case. Cases without a golden reference are skipped (there is
// nothing to rate against).
func Bakeoff(ctx context.Context, dir, root string, only string) []BakeoffRow {
	var rows []BakeoffRow
	for _, c := range Manifest() {
		if only != "" && !strings.Contains(c.Name, only) && c.Decoder != only {
			continue
		}
		if c.Golden == "" {
			continue
		}
		if c.Local == "" {
			if _, err := os.Stat(filepath.Join(dir, c.File)); err != nil {
				continue
			}
		}
		g := Run(c, dir, root)
		rows = append(rows, BakeoffRow{c.Name, c.Decoder, "go", g.Expected, g.Got, g.Matched, len(g.Extra), g.Status, g.Note})
		for _, tool := range refToolsFor(c.Decoder) {
			r := RefRun(ctx, c, dir, root, tool)
			rows = append(rows, BakeoffRow{c.Name, c.Decoder, tool, r.Expected, r.Got, r.Matched, len(r.Extra), r.Status, r.Note})
		}
	}
	return rows
}

// FamilyVerdict aggregates the bake-off per decoder family: recall for the
// Go decoder and the best reference tool, and the keep/switch call at the
// 2% tolerance.
type FamilyVerdict struct {
	Family    string
	GoRecall  float64 // matched/expected over the family's cases
	BestTool  string
	RefRecall float64
	KeepGo    bool // Go recall >= 0.98 * ref recall
}

// Verdicts aggregates bake-off rows into per-family calls.
func Verdicts(rows []BakeoffRow) []FamilyVerdict {
	type agg struct{ matched, expected int }
	goAgg := map[string]*agg{}
	refAgg := map[string]map[string]*agg{}
	for _, r := range rows {
		if r.Status == Skip {
			continue
		}
		if r.Tool == "go" {
			a := goAgg[r.Family]
			if a == nil {
				a = &agg{}
				goAgg[r.Family] = a
			}
			a.matched += r.Matched
			a.expected += r.Expected
			continue
		}
		m := refAgg[r.Family]
		if m == nil {
			m = map[string]*agg{}
			refAgg[r.Family] = m
		}
		a := m[r.Tool]
		if a == nil {
			a = &agg{}
			m[r.Tool] = a
		}
		a.matched += r.Matched
		a.expected += r.Expected
	}
	var out []FamilyVerdict
	fams := map[string]bool{}
	for f := range goAgg {
		fams[f] = true
	}
	for f := range refAgg {
		fams[f] = true
	}
	for f := range fams {
		v := FamilyVerdict{Family: f, KeepGo: true}
		if a := goAgg[f]; a != nil && a.expected > 0 {
			v.GoRecall = float64(a.matched) / float64(a.expected)
		}
		best := -1.0
		for tool, a := range refAgg[f] {
			if a.expected == 0 {
				continue
			}
			rec := float64(a.matched) / float64(a.expected)
			if rec > best {
				best, v.BestTool, v.RefRecall = rec, tool, rec
			}
		}
		if v.BestTool != "" && v.GoRecall < 0.98*v.RefRecall {
			v.KeepGo = false
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Family < out[j].Family })
	return out
}

// WriteBakeoff prints the bake-off table and the verdict block.
func WriteBakeoff(w io.Writer, rows []BakeoffRow, vs []FamilyVerdict) {
	fmt.Fprintf(w, "%-28s %-7s %-11s %7s %5s %7s %6s %s\n", "CASE", "FAMILY", "TOOL", "EXPECT", "GOT", "MATCHED", "EXTRA", "STATUS")
	for _, r := range rows {
		fmt.Fprintf(w, "%-28s %-7s %-11s %7d %5d %7d %6d %s\n", r.Case, r.Family, r.Tool, r.Expected, r.Got, r.Matched, r.Extra, r.Status)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "verdicts (keep Go only where recall is within 2% of the best reference):")
	for _, v := range vs {
		call := "KEEP GO"
		if !v.KeepGo {
			call = "PREFER " + v.BestTool
		}
		fmt.Fprintf(w, "  %-8s go %.1f%%  best-ref %s %.1f%%  -> %s\n", v.Family, 100*v.GoRecall, v.BestTool, 100*v.RefRecall, call)
	}
}
