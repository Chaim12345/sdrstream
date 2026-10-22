// Command decoderegress runs the decoders over public real recordings and
// prints a pass/fail table of decode counts against reference decodes.
//
//	decoderegress [-cache dir] [-fetch] [-only substr] [-v] [-json]
//
// Exit status is 1 when any case FAILs. GAP (known, tracked shortfall) and
// INFO cases do not fail the run.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/regress"
)

func main() {
	cache := flag.String("cache", filepath.Join(os.TempDir(), "sdrstream-regress"), "recording cache directory")
	fetch := flag.Bool("fetch", false, "download missing recordings (one request per -delay)")
	delay := flag.Duration("delay", time.Second, "pause after each download")
	only := flag.String("only", "", "run cases whose name or decoder contains this")
	verbose := flag.Bool("v", false, "list missed and invented messages")
	asJSON := flag.Bool("json", false, "emit JSON instead of the table")
	root := flag.String("root", ".", "go/ module directory (for committed recordings)")
	wb := flag.Bool("write-baseline", false, "print a new golden/baseline.txt from the current results")
	bakeoff := flag.Bool("bakeoff", false, "run Go decoders and reference tools side by side against the goldens")
	flag.Parse()

	if *bakeoff {
		rows := regress.Bakeoff(context.Background(), *cache, *root, *only)
		regress.WriteBakeoff(os.Stdout, rows, regress.Verdicts(rows))
		return
	}
	var results []regress.Result
	for _, c := range regress.Manifest() {
		if *only != "" && !strings.Contains(c.Name, *only) && c.Decoder != *only {
			continue
		}
		if *fetch {
			if err := regress.Fetch(c, *cache, *delay); err != nil {
				fmt.Fprintln(os.Stderr, "fetch:", err)
			}
		}
		if c.Local == "" {
			if _, err := os.Stat(filepath.Join(*cache, c.File)); err != nil {
				results = append(results, regress.Result{Case: c, Status: regress.Skip, Note: "not in cache (run with -fetch)"})
				continue
			}
		}
		results = append(results, regress.Run(c, *cache, *root))
	}
	if *wb {
		regress.WriteBaseline(os.Stdout, results)
		return
	}
	if *asJSON {
		json.NewEncoder(os.Stdout).Encode(results)
	} else {
		regress.WriteTable(os.Stdout, results)
		if *verbose {
			regress.WriteDetails(os.Stdout, results)
		}
	}
	for _, r := range results {
		if r.Status == regress.Fail {
			os.Exit(1)
		}
	}
}
