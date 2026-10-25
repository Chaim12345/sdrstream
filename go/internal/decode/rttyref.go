package decode

// Independent RTTY cross-check through a reference subprocess. The Go RTTY
// decoder's known failure mode is readable-looking false text from a strong
// carrier; a second, unrelated demodulator agreeing on the same text is the
// guard against publishing that. This file runs the reference tool as an
// unmodified child process exchanging data only through a WAV file and
// stdout (mere aggregation; see go/docs/EXTERNAL-DECODERS.md for the
// licensing model).
//
// Tool choice: minimodem is the reference engine. multimon-ng ships no RTTY
// demodulator at all, and fldigi has no non-interactive batch mode that
// decodes a WAV file to stdout, so neither can fill this role; minimodem
// (GPL-3.0+, Kamal Mostafa) decodes Baudot RTTY from a file in one shot.
//
// Resolution order for the binary, mirroring reference.go:
//  1. explicit path passed by the caller,
//  2. SDRSTREAM_REF_MINIMODEM,
//  3. SDRSTREAM_REFDEC_DIR (<dir>/usr/bin/minimodem or <dir>/minimodem),
//  4. $PATH.
//
// When the binary lives in an extracted-package layout (<prefix>/usr/bin
// with libraries under <prefix>/usr/lib/<triplet>) the bundled library dir
// is prepended to LD_LIBRARY_PATH for the child only.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// MinimodemBinary resolves an explicit path or the search order above.
// Returns "" when no minimodem is available.
func MinimodemBinary(explicit string) string {
	for _, cand := range minimodemCandidates(explicit) {
		if cand == "" {
			continue
		}
		if strings.ContainsRune(cand, os.PathSeparator) {
			if st, err := os.Stat(cand); err == nil && !st.IsDir() {
				return cand
			}
			continue
		}
		if p, err := exec.LookPath(cand); err == nil {
			return p
		}
	}
	return ""
}

func minimodemCandidates(explicit string) []string {
	out := []string{explicit, os.Getenv("SDRSTREAM_REF_MINIMODEM")}
	if dir := os.Getenv("SDRSTREAM_REFDEC_DIR"); dir != "" {
		out = append(out, filepath.Join(dir, "usr", "bin", "minimodem"), filepath.Join(dir, "minimodem"))
	}
	out = append(out, "minimodem")
	return out
}

// minimodemLibDir finds the extracted-package library dir for a binary in
// <prefix>/usr/bin, for the child process only.
func minimodemLibDir(binary string) string {
	if filepath.Base(binary) != "minimodem" || filepath.Base(filepath.Dir(binary)) != "bin" {
		return ""
	}
	prefix := filepath.Dir(filepath.Dir(binary)) // .../usr
	if filepath.Base(prefix) != "usr" {
		return ""
	}
	root := filepath.Dir(prefix)
	triplet := "x86_64-linux-gnu"
	if strings.Contains(strings.ToLower(os.Getenv("GOARCH")), "arm64") {
		triplet = "aarch64-linux-gnu"
	}
	for _, d := range []string{filepath.Join(root, "usr", "lib", triplet), filepath.Join(root, "usr", "lib"), filepath.Join(root, "lib", triplet)} {
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			return d
		}
	}
	return ""
}

// RTTYRefResult is one reference decode: the recovered text and the engine
// that produced it.
type RTTYRefResult struct {
	Text   string
	Engine string // "minimodem"
}

// RTTYRefDecode runs minimodem over wavPath with the given tone and baud
// parameters. markHz/spaceHz are the AUDIO tones the target defines (for
// inverted services such as DWD F1B the caller already passes mark as the
// lower tone); baud <= 0 defaults to 45.45. The child gets a two-minute
// ceiling; a partial decode on child error is still returned when the
// engine produced output.
func RTTYRefDecode(ctx context.Context, binary, wavPath string, markHz, spaceHz, baud float64) (RTTYRefResult, error) {
	binary = MinimodemBinary(binary)
	if binary == "" {
		return RTTYRefResult{}, fmt.Errorf("minimodem not installed")
	}
	if markHz == 0 {
		markHz = 2125
	}
	if spaceHz == 0 {
		spaceHz = markHz - 170
	}
	if baud <= 0 {
		baud = 45.45
	}
	args := []string{
		"--rx", "-q",
		"-f", wavPath,
		"-M", strconv.FormatFloat(markHz, 'f', -1, 64),
		"-S", strconv.FormatFloat(spaceHz, 'f', -1, 64),
		"--startbits", "1", "--stopbits", "1.5", "--baudot",
	}
	// Positional baudmode: the "rtty" keyword pins 45.45; any whole baud
	// rate is given as a number.
	if baud == 45.45 {
		args = append(args, "rtty")
	} else {
		args = append(args, strconv.FormatFloat(baud, 'f', -1, 64))
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	// A grandchild inheriting the pipes must not hang Wait after the
	// decoder itself is gone.
	cmd.WaitDelay = 10 * time.Second
	if lib := minimodemLibDir(binary); lib != "" {
		cmd.Env = append(os.Environ(), "LD_LIBRARY_PATH="+lib+string(os.PathListSeparator)+os.Getenv("LD_LIBRARY_PATH"))
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	text := strings.TrimSpace(stdout.String())
	if err != nil && text == "" {
		return RTTYRefResult{}, fmt.Errorf("minimodem: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return RTTYRefResult{Text: text, Engine: "minimodem"}, nil
}
