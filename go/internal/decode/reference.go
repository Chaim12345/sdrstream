package decode

// Reference decoders: unmodified upstream tools (WSJT-X jt9/wsprd,
// kgoba/ft8_lib decode_ft8, multimon-ng, TLeconte/acarsdec) run as separate
// subprocesses behind one interface. They serve two purposes: the
// decoderegress bake-off that rates the Go decoders against the references,
// and optional preferred decoding at runtime (see engine.go).
//
// Licensing: every tool runs as an unmodified child process over WAV files
// (mere aggregation); nothing is linked or copied into sdrstream. See
// go/docs/EXTERNAL-DECODERS.md for the per-tool licences and build notes.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// RefMessage is one decode from a reference tool.
type RefMessage struct {
	Text   string  // message text ("CQ K1JT FN20", "F4BAD JN25 20", ...)
	SNR    float64 // dB as reported by the tool
	DT     float64 // time offset vs nominal slot/window start, seconds
	FreqHz float64 // audio frequency in Hz
	Raw    string  // the output line as printed (without prefix)
}

// RefDecoder is one external reference decoder.
type RefDecoder interface {
	// Name is the registry key: "jt9", "wsprd", "ft8lib", "multimon-ng", "acarsdec".
	Name() string
	// Binary resolves the executable path, "" when unavailable.
	Binary() string
	// Modes lists the decoder families it covers: ft8, ft4, wspr,
	// pocsag, flex, aprs, morse, acars.
	Modes() []string
	// DecodeWAV decodes a WAV file. mode selects the variant for tools
	// that cover several families ("ft8"/"ft4" for jt9/ft8lib,
	// "POCSAG512" for multimon-ng); it is ignored otherwise.
	DecodeWAV(ctx context.Context, wavPath, mode string) ([]RefMessage, error)
}

// refBin resolves a tool executable: explicit path, then
// SDRSTREAM_REF_<NAME>, then SDRSTREAM_REFDEC_DIR (usr/bin layout or flat),
// then PATH.
func refBin(explicit, envName, fileName string) string {
	if explicit != "" {
		if p, err := exec.LookPath(explicit); err == nil {
			return p
		}
		if st, err := os.Stat(explicit); err == nil && !st.IsDir() {
			return explicit
		}
		return ""
	}
	if v := os.Getenv("SDRSTREAM_REF_" + envName); v != "" {
		if st, err := os.Stat(v); err == nil && !st.IsDir() {
			return v
		}
	}
	if dir := os.Getenv("SDRSTREAM_REFDEC_DIR"); dir != "" {
		for _, cand := range []string{
			filepath.Join(dir, "usr", "bin", fileName),
			filepath.Join(dir, fileName),
		} {
			if st, err := os.Stat(cand); err == nil && !st.IsDir() {
				return cand
			}
		}
	}
	p, _ := exec.LookPath(fileName)
	return p
}

// refEnv builds the environment for a reference tool: when the executable
// lives under a <prefix>/usr/bin with bundled libraries (the extracted .deb
// layout), those libraries are prepended to LD_LIBRARY_PATH so the bundled
// copies win without discarding an existing value.
func refEnv(binaryPath string) []string {
	dir := filepath.Dir(binaryPath) // .../usr/bin or flat
	prefix := filepath.Dir(dir)     // .../usr or parent
	var libdirs []string
	for _, pat := range []string{
		filepath.Join(prefix, "lib", "*-linux-gnu"),
		filepath.Join(dir, "..", "lib", "*-linux-gnu"),
		filepath.Join(prefix, "lib"),
	} {
		matches, _ := filepath.Glob(pat)
		libdirs = append(libdirs, matches...)
	}
	if len(libdirs) == 0 {
		return nil
	}
	ld := strings.Join(libdirs, ":")
	if existing := os.Getenv("LD_LIBRARY_PATH"); existing != "" {
		ld += ":" + existing
	}
	return append(os.Environ(), "LD_LIBRARY_PATH="+ld)
}

// runRef executes a reference tool with bounded time and a private working
// directory (wsprd and jt9 write scratch files into the cwd).
func runRef(ctx context.Context, env []string, timeout time.Duration, binaryPath string, args ...string) (string, string, error) {
	workdir, err := os.MkdirTemp("", "sdrstream-ref-")
	if err != nil {
		return "", "", err
	}
	defer os.RemoveAll(workdir)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, binaryPath, args...)
	cmd.Dir = workdir
	cmd.WaitDelay = 10 * time.Second
	if env != nil {
		cmd.Env = env
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	return stdout.String(), stderr.String(), err
}

// absWAV makes the wav path absolute because ref tools run in a temp cwd.
func absWAV(p string) (string, error) {
	return filepath.Abs(p)
}

// ---- jt9 (WSJT-X): FT8 (-8), FT4 (-4), JT65 (-65), JT9 (-9) ----

type jt9Ref struct{ path string }

// JT9 returns the WSJT-X jt9 reference decoder.
func JT9(path string) RefDecoder { return jt9Ref{path: path} }

func (j jt9Ref) Name() string { return "jt9" }
func (j jt9Ref) Binary() string {
	return refBin(j.path, "JT9", "jt9")
}
func (j jt9Ref) Modes() []string { return []string{"ft8", "ft4", "jt65", "jt9"} }

var jt9Line = regexp.MustCompile(`^(\d{6})\s+(-?\d+)\s+(-?[\d.]+)\s+(\d+)\s+~\s+(.+)$`)

func (j jt9Ref) DecodeWAV(ctx context.Context, wavPath, mode string) ([]RefMessage, error) {
	bin := j.Binary()
	if bin == "" {
		return nil, fmt.Errorf("jt9 not installed")
	}
	flag := "-8"
	switch strings.ToLower(mode) {
	case "ft8", "":
	case "ft4":
		flag = "-4"
	case "jt65":
		flag = "-65"
	case "jt9":
		flag = "-9"
	default:
		return nil, fmt.Errorf("jt9: unsupported mode %q", mode)
	}
	p, err := absWAV(wavPath)
	if err != nil {
		return nil, err
	}
	out, serr, err := runRef(ctx, refEnv(bin), 10*time.Minute, bin, flag, "-d", "3", p)
	if err != nil && out == "" {
		return nil, fmt.Errorf("jt9: %v: %s", err, strings.TrimSpace(serr))
	}
	var msgs []RefMessage
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r \t")
		if strings.HasPrefix(line, "<DecodeFinished>") {
			break
		}
		m := jt9Line.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		snr, _ := strconv.ParseFloat(m[2], 64)
		dt, _ := strconv.ParseFloat(m[3], 64)
		freq, _ := strconv.ParseFloat(m[4], 64)
		msgs = append(msgs, RefMessage{
			Text: strings.Join(strings.Fields(m[5]), " "), SNR: snr, DT: dt, FreqHz: freq, Raw: line,
		})
	}
	return msgs, nil
}

// ---- wsprd (WSJT-X) ----

type wsprdRef struct{ path string }

// WSPRD returns the WSJT-X wsprd reference decoder.
func WSPRD(path string) RefDecoder { return wsprdRef{path: path} }

func (w wsprdRef) Name() string    { return "wsprd" }
func (w wsprdRef) Modes() []string { return []string{"wspr"} }
func (w wsprdRef) Binary() string  { return refBin(w.path, "WSPRD", "wsprd") }

// wsprd stdout: "<hhmm|d> <snr> <dt> <freqMHz> <drift> <call> <grid> <dbm>".
// The time column degrades to "d" when the filename carries no timestamp.
func (w wsprdRef) DecodeWAV(ctx context.Context, wavPath, _ string) ([]RefMessage, error) {
	bin := w.Binary()
	if bin == "" {
		return nil, fmt.Errorf("wsprd not installed")
	}
	p, err := absWAV(wavPath)
	if err != nil {
		return nil, err
	}
	out, serr, err := runRef(ctx, refEnv(bin), 10*time.Minute, bin, p)
	if err != nil && out == "" {
		return nil, fmt.Errorf("wsprd: %v: %s", err, strings.TrimSpace(serr))
	}
	var msgs []RefMessage
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r \t")
		if strings.HasPrefix(line, "<DecodeFinished>") {
			break
		}
		f := strings.Fields(line)
		if len(f) < 6 {
			continue
		}
		snr, err1 := strconv.ParseFloat(f[1], 64)
		dt, err2 := strconv.ParseFloat(f[2], 64)
		fMHz, err3 := strconv.ParseFloat(f[3], 64)
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}
		msgs = append(msgs, RefMessage{
			Text: strings.Join(f[5:], " "), SNR: snr, DT: dt, FreqHz: fMHz * 1e6, Raw: line,
		})
	}
	return msgs, nil
}

// ---- decode_ft8 (kgoba/ft8_lib, MIT) ----

type ft8libRef struct{ path string }

// FT8Lib returns the kgoba/ft8_lib demo decoder (MIT).
func FT8Lib(path string) RefDecoder { return ft8libRef{path: path} }

func (f ft8libRef) Name() string    { return "ft8lib" }
func (f ft8libRef) Modes() []string { return []string{"ft8", "ft4"} }
func (f ft8libRef) Binary() string  { return refBin(f.path, "FT8LIB", "decode_ft8") }

var ft8libLine = regexp.MustCompile(`^(\d{6})\s+([-+]?[\d.]+)\s+([-+]?[\d.]+)\s+(\d+)\s+~\s+(.+)$`)

func (f ft8libRef) DecodeWAV(ctx context.Context, wavPath, mode string) ([]RefMessage, error) {
	bin := f.Binary()
	if bin == "" {
		return nil, fmt.Errorf("decode_ft8 (ft8_lib) not installed")
	}
	args := []string{}
	if strings.EqualFold(mode, "ft4") {
		args = append(args, "-ft4")
	}
	p, err := absWAV(wavPath)
	if err != nil {
		return nil, err
	}
	out, serr, err := runRef(ctx, nil, 5*time.Minute, bin, append(args, p)...)
	if err != nil && out == "" {
		return nil, fmt.Errorf("decode_ft8: %v: %s", err, strings.TrimSpace(serr))
	}
	var msgs []RefMessage
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r \t")
		m := ft8libLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		snr, _ := strconv.ParseFloat(m[2], 64)
		dt, _ := strconv.ParseFloat(m[3], 64)
		freq, _ := strconv.ParseFloat(m[4], 64)
		msgs = append(msgs, RefMessage{
			Text: strings.Join(strings.Fields(m[5]), " "), SNR: snr, DT: dt, FreqHz: freq, Raw: line,
		})
	}
	return msgs, nil
}

// ---- multimon-ng: POCSAG, FLEX, AFSK1200 (APRS), MORSE_CW ----

type multimonRef struct{ path string }

// Multimon returns the multimon-ng reference decoder.
func Multimon(path string) RefDecoder { return multimonRef{path: path} }

func (m multimonRef) Name() string    { return "multimon-ng" }
func (m multimonRef) Modes() []string { return []string{"pocsag", "flex", "aprs", "morse"} }
func (m multimonRef) Binary() string  { return refBin(m.path, "MULTIMON", "multimon-ng") }

var mmPOCSAG = regexp.MustCompile(`^POCSAG\d+:\s+Address:\s*(\d+)\s+Function:\s*(\d)\s+(Alpha|Numeric|SKYPER):\s*(.*)$`)

// DecodeWAV runs multimon-ng; mode is the demodulator list, comma-separated
// (e.g. "POCSAG512" or "POCSAG512,POCSAG1200,POCSAG2400"), empty = AFSK1200.
func (m multimonRef) DecodeWAV(ctx context.Context, wavPath, mode string) ([]RefMessage, error) {
	var demods []string
	if mode != "" {
		demods = strings.Split(mode, ",")
	}
	lines, err := MultimonDecodeWithBinary(ctx, m.Binary(), wavPath, demods...)
	if err != nil {
		return nil, err
	}
	var msgs []RefMessage
	for _, line := range lines {
		rm := RefMessage{Text: line, Raw: line}
		if pm := mmPOCSAG.FindStringSubmatch(line); pm != nil {
			rm.Text = pm[1] + "|" + pm[2] + "|" + strings.TrimSpace(pm[4])
		} else if strings.HasPrefix(line, "FLEX|") {
			// FLEX|date time|1600/2/K/A|14.116|001120103|ALN|message
			f := strings.SplitN(line, "|", 7)
			if len(f) == 7 {
				cap, _ := strconv.ParseInt(f[4], 10, 64)
				rm.Text = fmt.Sprintf("%d|%s", cap, strings.TrimSpace(f[6]))
			}
		} else if strings.HasPrefix(strings.TrimPrefix(line, "AFSK1200: "), "fm ") {
			line = strings.TrimPrefix(line, "AFSK1200: ")
			// AFSK1200 APRS: "fm SRC to DST [via PATH] UIv pid=F0"
			f := strings.Fields(line)
			if len(f) >= 4 && f[2] == "to" {
				rm.Text = f[1] + ">" + f[3]
			}
		} else if i := strings.Index(line, ">"); i > 0 && strings.Contains(line, ":") && !strings.Contains(line, "|") {
			// APRS "SRC>DEST[,PATH]:..." -> "SRC>DEST"
			head := line[:strings.Index(line, ":")]
			if j := strings.Index(head, ","); j > 0 {
				head = head[:j]
			}
			rm.Text = head
		}
		msgs = append(msgs, rm)
	}
	return msgs, nil
}

// ---- acarsdec (TLeconte/acarsdec, LGPL-2.0, built WITH_SNDFILE) ----

const acarsdecRate = 12500 // INTRATE; WAV input must be exactly this rate

type acarsdecRef struct{ path string }

// Acarsdec returns the acarsdec reference decoder (WAV input at 12500 Hz).
func Acarsdec(path string) RefDecoder { return acarsdecRef{path: path} }

func (a acarsdecRef) Name() string    { return "acarsdec" }
func (a acarsdecRef) Modes() []string { return []string{"acars"} }
func (a acarsdecRef) Binary() string  { return refBin(a.path, "ACARSDEC", "acarsdec") }

type acarsdecJSON struct {
	Channel int     `json:"channel"`
	Mode    string  `json:"mode"`
	Label   string  `json:"label"`
	BlockID string  `json:"block_id"`
	Tail    string  `json:"tail"`
	Level   float64 `json:"level"`
	Err     int     `json:"error"`
}

// DecodeWAV decodes a WAV whose rate must already be 12500 Hz (acarsdec's
// fixed INTRATE). Use AcarsdecWAV to resample arbitrary audio first. mode is
// ignored. Messages with bit errors (error != 0) are dropped.
func (a acarsdecRef) DecodeWAV(ctx context.Context, wavPath, _ string) ([]RefMessage, error) {
	bin := a.Binary()
	if bin == "" {
		return nil, fmt.Errorf("acarsdec not installed")
	}
	p, err := absWAV(wavPath)
	if err != nil {
		return nil, err
	}
	out, serr, err := runRef(ctx, refEnv(bin), 5*time.Minute, bin, "-o", "4", "-f", p)
	if err != nil && out == "" {
		return nil, fmt.Errorf("acarsdec: %v: %s", err, strings.TrimSpace(serr))
	}
	var msgs []RefMessage
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var j acarsdecJSON
		if json.Unmarshal([]byte(line), &j) != nil || j.Err != 0 || j.Tail == "" {
			continue
		}
		mode := j.Mode
		if mode == "" {
			mode = "x"
		}
		text := fmt.Sprintf("%s|%s|%s|%s", j.Tail, j.Label, mode, j.BlockID)
		msgs = append(msgs, RefMessage{Text: text, SNR: j.Level, Raw: line})
	}
	return msgs, nil
}

// AcarsdecWAV writes samples as a mono 12500 Hz WAV for acarsdec and returns
// its path (in dir, or a temp dir when dir is "").
func AcarsdecWAV(samples []float64, rate int, dir string) (string, error) {
	if rate <= 0 {
		return "", fmt.Errorf("bad sample rate")
	}
	rs := resampleLinear(samples, rate, acarsdecRate)
	pcm := floatTo16(rs)
	if dir == "" {
		var err error
		if dir, err = os.MkdirTemp("", "sdrstream-acars-"); err != nil {
			return "", err
		}
	}
	p := filepath.Join(dir, "acars_12500.wav")
	return p, os.WriteFile(p, WriteWAV16(pcm, acarsdecRate), 0o644)
}

// floatTo16 scales normalised floats to PCM samples.
func floatTo16(samples []float64) []int16 {
	pcm := make([]int16, len(samples))
	for i, s := range samples {
		if math.IsNaN(s) || math.IsInf(s, 0) {
			s = 0
		}
		if s > 1 {
			s = 1
		}
		if s < -1 {
			s = -1
		}
		pcm[i] = int16(math.Round(s * 32767))
	}
	return pcm
}

// RefDecoders returns the registry of all known reference decoders.
// Binaries resolve lazily; check Binary() != "" for availability.
func RefDecoders() []RefDecoder {
	return []RefDecoder{JT9(""), WSPRD(""), FT8Lib(""), Multimon(""), Acarsdec("")}
}

// RefAvailable reports which reference decoders resolve an executable.
func RefAvailable() map[string]bool {
	out := map[string]bool{}
	for _, d := range RefDecoders() {
		out[d.Name()] = d.Binary() != ""
	}
	return out
}
