package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/chaim12345/sdrstream/go/internal/web"
)

// cmdCapturemeta validates capture metadata sidecars (.txt next to .wav)
// against docs/capture-sidecar.md so a hand-written or imported sidecar
// cannot assert provenance the schema does not support.
func cmdCapturemeta(args []string) error {
	fs := flag.NewFlagSet("capturemeta", flag.ExitOnError)
	check := fs.Bool("check", false, "validate sidecar files or directories of them")
	fs.Parse(args)
	if !*check || fs.NArg() == 0 {
		return fmt.Errorf("usage: sdrstream capturemeta -check <sidecar.txt|dir> [...]")
	}
	failures, checked := 0, 0
	for _, arg := range fs.Args() {
		var files []string
		if st, err := os.Stat(arg); err == nil && st.IsDir() {
			entries, _ := os.ReadDir(arg)
			for _, e := range entries {
				if strings.HasSuffix(e.Name(), ".txt") {
					files = append(files, filepath.Join(arg, e.Name()))
				}
			}
		} else {
			files = append(files, arg)
		}
		for _, f := range files {
			data, err := os.ReadFile(f)
			if err != nil {
				fmt.Printf("%s: error: %v\n", f, err)
				failures++
				continue
			}
			checked++
			issues := web.ValidateSidecar(data)
			for _, issue := range issues {
				fmt.Printf("%s: %s\n", f, issue)
				if issue.Level == "error" {
					failures++
				}
			}
			if len(issues) == 0 {
				fmt.Printf("%s: ok\n", f)
			}
		}
	}
	fmt.Printf("capturemeta: %d sidecar(s) checked, %d error(s)\n", checked, failures)
	if failures > 0 {
		return fmt.Errorf("%d validation error(s)", failures)
	}
	return nil
}
