package regress

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

// WriteTable prints the pass/fail table: decode counts against expected.
func WriteTable(w io.Writer, rs []Result) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "STATUS\tCASE\tDECODER\tEXPECTED\tGOT\tMATCHED\tMISSED\tEXTRA\tBONUS\tUNSUP\tREFERENCE")
	for _, r := range rs {
		exp := fmt.Sprint(r.Expected)
		if r.Case.Golden == "" && r.Case.MinFrames == 0 {
			exp = "-"
		} else if r.Case.Golden == "" {
			exp = ">=" + exp
		}
		ref := r.Case.Ref
		if ref == "" {
			ref = "none"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%d\t%d\t%d\t%d\t%d\t%s\n", r.Status, r.Case.Name, r.Case.Decoder,
			exp, r.Got, r.Matched, len(r.Missed), len(r.Extra), len(r.Bonus), len(r.Unsupported), ref)
	}
	tw.Flush()
	fmt.Fprintln(w, "\n"+Summary(rs))
}

// WriteDetails lists missed and invented messages for every non-PASS case.
func WriteDetails(w io.Writer, rs []Result) {
	for _, r := range rs {
		if r.Status == Pass {
			continue
		}
		if len(r.Missed) == 0 && len(r.Extra) == 0 && len(r.Unsupported) == 0 && r.Note == "" {
			continue
		}
		fmt.Fprintf(w, "\n[%s] %s  %s\n", r.Status, r.Case.Name, r.Case.Source)
		if r.Note != "" {
			fmt.Fprintln(w, "  note:", r.Note)
		}
		for _, m := range r.Unsupported {
			fmt.Fprintln(w, "  unsupported-type decode:", trunc(m))
		}
		for _, m := range r.Missed {
			fmt.Fprintln(w, "  missed:", trunc(m))
		}
		if r.Case.Golden != "" || r.Status == Info {
			for _, m := range r.Extra {
				kind := "extra:"
				if r.Status == Info {
					kind = "decoded:"
				}
				fmt.Fprintln(w, " ", kind, trunc(m))
			}
		}
	}
}

func trunc(s string) string {
	if len(s) > 110 {
		return s[:107] + "..."
	}
	return strings.TrimSpace(s)
}
