package decode

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// External decoders: multimon-ng is the reference implementation for AFSK
// and CW and handles WAV files directly at any rate via sox-style
// resampling. When it is installed it is preferred over the built-in
// decoders, which exist so the binary is useful on its own.

// MultimonAvailable reports whether multimon-ng is on PATH.
func MultimonAvailable() bool {
	_, err := exec.LookPath("multimon-ng")
	return err == nil
}

// MultimonDecode runs multimon-ng over a WAV file with the given
// demodulators (e.g. "AFSK1200", "MORSE_CW") and returns its decoded
// lines, one per packet or message.
func MultimonDecode(ctx context.Context, path string, demodulators ...string) ([]string, error) {
	if !MultimonAvailable() {
		return nil, fmt.Errorf("multimon-ng not installed")
	}
	if len(demodulators) == 0 {
		demodulators = []string{"AFSK1200"}
	}
	args := []string{"-t", "wav", "-q"}
	for _, d := range demodulators {
		args = append(args, "-a", d)
	}
	args = append(args, path)

	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "multimon-ng", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil && stdout.Len() == 0 {
		return nil, fmt.Errorf("multimon-ng: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	var lines []string
	for _, line := range strings.Split(stdout.String(), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		// multimon prefixes packet lines with "APRS: ".
		lines = append(lines, strings.TrimPrefix(line, "APRS: "))
	}
	return lines, nil
}
