package regress

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// Baseline lines in golden/baseline.txt: "<case> <floor> <maxextra>". They record what the
// decoders reproduce today. A case fails when it drops below its floor or
// produces more unconfirmed extras than recorded. Cases without a line must
// match their reference exactly.
func baseline() map[string][2]int {
	out := map[string][2]int{}
	b, err := goldenFS.ReadFile("golden/baseline.txt")
	if err != nil {
		return out
	}
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) != 3 || strings.HasPrefix(f[0], "#") {
			continue
		}
		fl, _ := strconv.Atoi(f[1])
		mx, _ := strconv.Atoi(f[2])
		out[f[0]] = [2]int{fl, mx}
	}
	return out
}

// WriteBaseline prints a baseline file for the given results.
func WriteBaseline(w io.Writer, rs []Result) {
	fmt.Fprintln(w, "# case floor maxextra - regenerate with: decoderegress -write-baseline (review the diff!)")
	sort.Slice(rs, func(i, j int) bool { return rs[i].Case.Name < rs[j].Case.Name })
	for _, r := range rs {
		if r.Case.Golden == "" || r.Status == Skip {
			continue
		}
		fmt.Fprintf(w, "%s %d %d\n", r.Case.Name, r.Matched, len(r.Extra))
	}
}
