package decode

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// External decoders: multimon-ng is the reference implementation for AFSK
// and CW and handles WAV files directly at any rate via sox-style
// resampling. When it is installed it is preferred over the built-in
// decoders, which exist so the binary is useful on its own.

// MultimonAvailable reports whether multimon-ng is on PATH.
func MultimonAvailable() bool { return MultimonBinary("") != "" }

// MultimonBinary resolves an explicit executable or an executable on PATH.
func MultimonBinary(path string) string {
	if path != "" {
		if p, err := exec.LookPath(path); err == nil {
			return p
		}
		if st, err := os.Stat(path); err == nil && !st.IsDir() {
			return path
		}
		return ""
	}
	p, _ := exec.LookPath("multimon-ng")
	return p
}

// MultimonDecode runs multimon-ng over a WAV file with the given
// demodulators (e.g. "AFSK1200", "MORSE_CW") and returns its decoded
// lines, one per packet or message.
func MultimonDecode(ctx context.Context, path string, demodulators ...string) ([]string, error) {
	return MultimonDecodeWithBinary(ctx, "", path, demodulators...)
}

// MultimonDecodeWithBinary invokes an optional multimon-ng executable.
func MultimonDecodeWithBinary(ctx context.Context, binaryPath, path string, demodulators ...string) ([]string, error) {
	binaryPath = MultimonBinary(binaryPath)
	if binaryPath == "" {
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
	cmd := exec.CommandContext(ctx, binaryPath, args...)
	// A grandchild that inherits the pipes must not hang Wait after the
	// decoder itself is gone.
	cmd.WaitDelay = 10 * time.Second
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

// DirewolfBinary resolves an explicit executable or the PATH installation.
func DirewolfBinary(path string) string {
	if path != "" {
		if p, err := exec.LookPath(path); err == nil {
			return p
		}
		if st, err := os.Stat(path); err == nil && !st.IsDir() {
			return path
		}
		return ""
	}
	p, _ := exec.LookPath("direwolf")
	return p
}

// DirewolfDecode runs Dire Wolf against in-memory mono PCM using a temporary
// receive-only config and stdin. No network, radio control, or transmit path
// is configured. Only valid-looking AX.25 address lines are returned.
func DirewolfDecode(ctx context.Context, binaryPath string, samples []float64, rate int) ([]string, error) {
	path := DirewolfBinary(binaryPath)
	if path == "" {
		return nil, fmt.Errorf("direwolf not installed")
	}
	if rate < 8000 || rate > 48000 || rate%100 != 0 {
		return nil, fmt.Errorf("unsupported Dire Wolf sample rate %d", rate)
	}
	pcm := make([]byte, len(samples)*2)
	for i, sample := range samples {
		if math.IsNaN(sample) || math.IsInf(sample, 0) {
			sample = 0
		}
		if sample > 1 {
			sample = 1
		}
		if sample < -1 {
			sample = -1
		}
		v := int16(math.Round(sample * 32767))
		binary.LittleEndian.PutUint16(pcm[i*2:], uint16(v))
	}
	cfg, err := os.CreateTemp("", "sdrstream-direwolf-*.conf")
	if err != nil {
		return nil, err
	}
	cfgPath := cfg.Name()
	defer os.Remove(cfgPath)
	if _, err = cfg.WriteString("ADEVICE stdin null\nCHANNEL 0\nMYCALL NOCALL\n"); err != nil {
		cfg.Close()
		return nil, err
	}
	if err = cfg.Close(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "-c", cfgPath, "-r", fmt.Sprint(rate), "-n", "1", "-b", "16", "-t", "0", "-q", "h", "-")
	cmd.WaitDelay = 10 * time.Second
	if libdir := filepath.Join(filepath.Dir(filepath.Dir(path)), "lib"); isDir(libdir) {
		// Prepend so the bundled libraries win, but keep any path the
		// caller already configured instead of discarding it.
		ldpath := libdir
		if existing := os.Getenv("LD_LIBRARY_PATH"); existing != "" {
			ldpath += ":" + existing
		}
		cmd.Env = append(os.Environ(), "LD_LIBRARY_PATH="+ldpath)
	}
	cmd.Stdin = bytes.NewReader(pcm)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil && stdout.Len() == 0 {
		return nil, fmt.Errorf("direwolf: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	var lines []string
	for _, line := range strings.Split(stdout.String(), "\n") {
		line = strings.TrimSpace(line)
		if i := strings.Index(line, "] "); i >= 0 {
			line = line[i+2:]
		}
		if strings.Contains(line, ">") && strings.Contains(line, ":") {
			lines = append(lines, line)
		}
	}
	return lines, nil
}

func isDir(path string) bool { st, err := os.Stat(path); return err == nil && st.IsDir() }

// FFmpegBinary resolves an ffmpeg executable on PATH.
func FFmpegBinary(path string) string {
	if path != "" {
		if p, err := exec.LookPath(path); err == nil {
			return p
		}
		if st, err := os.Stat(path); err == nil && !st.IsDir() {
			return path
		}
		return ""
	}
	p, _ := exec.LookPath("ffmpeg")
	return p
}

// FFmpegAvailable reports whether ffmpeg is on PATH.
func FFmpegAvailable() bool { return FFmpegBinary("") != "" }

// ConvertToWAV converts any audio ffmpeg understands (SatNOGS publishes
// Ogg Vorbis) into a mono PCM WAV the decoders read. The native sample
// rate is kept; the decoders resample internally where it matters.
func ConvertToWAV(ctx context.Context, binaryPath, src, dst string) error {
	binaryPath = FFmpegBinary(binaryPath)
	if binaryPath == "" {
		return fmt.Errorf("ffmpeg not installed: cannot decode %s (install ffmpeg or supply a WAV)", filepath.Base(src))
	}
	if strings.EqualFold(filepath.Ext(src), ".wav") {
		data, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, data, 0o644)
	}
	cmd := exec.CommandContext(ctx, binaryPath, "-y", "-v", "error", "-i", src, "-ac", "1", dst)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg %s: %v: %s", filepath.Base(src), err, strings.TrimSpace(string(out)))
	}
	return nil
}
