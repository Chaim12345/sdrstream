package decode

import (
	"context"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/kiwisdr"
	"github.com/chaim12345/sdrstream/go/internal/verify"
)

// HFResult is the outcome of one monitored HF broadcast: the decoded
// payload, the gate verdict, and the provenance a gallery entry needs.
type HFResult struct {
	Target   HFTarget
	Server   string
	Started  time.Time
	Text     string // decoded text for rtty/cw/navtex
	ImagePNG []byte // decoded image for wefax/sstv
	AudioWAV []byte // raw capture, kept for audit regardless of verdict
	Gate     verify.Result
}

// Live reports whether the gate passed this result for the live gallery.
func (r HFResult) Live() bool { return r.Gate.Live }

// RunHFTarget listens to one HF broadcast on one KiwiSDR server and decodes
// it. Politeness contract: the caller serialises targets per server, one
// connection per server at a time, and keeps listen short; this function
// always closes its connection, including on error paths.
func RunHFTarget(ctx context.Context, server string, target HFTarget, listen time.Duration) (HFResult, error) {
	started := time.Now().UTC()
	opts := kiwisdr.DefaultOptions()
	opts.OpenTimeout = 10 * time.Second
	opts.IdleTimeout = 13 * time.Second
	client, err := kiwisdr.New(server, opts)
	if err != nil {
		return HFResult{Target: target, Server: server, Started: started}, err
	}
	defer client.Close()
	if err := client.Connect(ctx); err != nil {
		return HFResult{Target: target, Server: server, Started: started}, fmt.Errorf("connect %s: %w", server, err)
	}
	if err := client.Tune(ctx, int64(target.DialKHz*1000), target.Mode); err != nil {
		return HFResult{Target: target, Server: server, Started: started}, fmt.Errorf("tune: %w", err)
	}
	samples16, rate, err := client.Record(ctx, listen)
	if err != nil {
		return HFResult{Target: target, Server: server, Started: started}, fmt.Errorf("record: %w", err)
	}
	result := decodeHF(target, server, samples16, rate)
	result.Started = started
	result.AudioWAV = WriteWAV16(samples16, rate)
	return result, nil
}

// SessionOptions controls a multi-target visit to one receiver. The
// politeness contract is encoded here: one connection, sequential targets,
// short listens, and a total session time under the receiver's own limit
// (KiwiSDR servers kick long sessions, so default 5 minutes).
type SessionOptions struct {
	Listen          time.Duration // per-target listen; default 12s
	MaxTargets      int           // per connection; default 20
	MaxSession      time.Duration // per connection; default 5 min
	SkipInactive    bool          // skip targets whose schedule says off-air
	RecordFullAudio bool          // keep AudioWAV on every result, not only failures
	WSPath          string        // preflight-selected Kiwi WebSocket path style
	// AllowPrivate permits dialing loopback/private hosts (local receivers,
	// tests). Leave false for directory-sourced stations.
	AllowPrivate bool
}

func (o *SessionOptions) defaults() {
	if o.Listen <= 0 {
		o.Listen = 12 * time.Second
	}
	if o.MaxTargets <= 0 {
		o.MaxTargets = 20
	}
	if o.MaxSession <= 0 {
		o.MaxSession = 5 * time.Minute
	}
}

// RunHFSession visits one server once and works through targets in order,
// one connection per server, retuning between targets. It stops at
// MaxTargets or when MaxSession would be exceeded, whichever comes first;
// remaining targets are returned so the caller can reconnect later.
// Connection failures and refusals end the session: the caller backs off
// and skips this station rather than retrying inside the session.
func RunHFSession(ctx context.Context, server string, targets []HFTarget, opts SessionOptions) (results []HFResult, remaining []HFTarget, err error) {
	opts.defaults()
	now := time.Now()
	if opts.SkipInactive {
		active := targets[:0]
		for _, t := range targets {
			if t.ActiveNow(now) {
				active = append(active, t)
			}
		}
		targets = active
	}
	if len(targets) > opts.MaxTargets {
		remaining = append(remaining, targets[opts.MaxTargets:]...)
		targets = targets[:opts.MaxTargets]
	}
	kiwiOptions := kiwisdr.DefaultOptions()
	kiwiOptions.WSPath = opts.WSPath
	kiwiOptions.AllowPrivate = opts.AllowPrivate
	client, err := kiwisdr.New(server, kiwiOptions)
	if err != nil {
		return nil, targets, err
	}
	if err := client.Connect(ctx); err != nil {
		return nil, targets, fmt.Errorf("connect %s: %w", server, err)
	}
	defer client.Close()
	sessionStart := time.Now()
	for i, target := range targets {
		if time.Since(sessionStart)+target.ListenFor(opts.Listen) > opts.MaxSession {
			remaining = append(remaining, targets[i:]...)
			break
		}
		if err := client.Tune(ctx, int64(target.DialKHz*1000), target.Mode); err != nil {
			remaining = append(remaining, targets[i:]...)
			return results, remaining, fmt.Errorf("tune: %w", err)
		}
		samples16, rate, rerr := client.Record(ctx, target.ListenFor(opts.Listen))
		if rerr != nil {
			remaining = append(remaining, targets[i:]...)
			return results, remaining, fmt.Errorf("record: %w", rerr)
		}
		result := decodeHF(target, server, samples16, rate)
		if !result.Gate.Live || opts.RecordFullAudio {
			result.AudioWAV = WriteWAV16(samples16, rate)
		}
		results = append(results, result)
	}
	return results, remaining, nil
}

// decodeHF runs the decoder and gate for one captured target. Kinds
// without a built-in decoder (voice, dsc, hfdl, ft8, wspr) are real
// captures but must never publish: the gate rejects them here.
// HFDecodeForScan decodes already captured samples using the target's decoder and quality gate.
func HFDecodeForScan(target HFTarget, server string, samples16 []int16, rate int) HFResult {
	return decodeHF(target, server, samples16, rate)
}

func decodeHF(target HFTarget, server string, samples16 []int16, rate int) HFResult {
	result := HFResult{Target: target, Server: server, Started: time.Now().UTC()}
	samples := make([]float64, len(samples16))
	for i, s := range samples16 {
		samples[i] = float64(s) / 32768.0
	}
	switch target.Kind {
	case "rtty":
		text, _ := RTTYDecodeAuto(samples, rate, target.MarkHz, target.SpaceHz, target.Baud)
		result.Text = text
		result.Gate = verify.Text("rtty", text, target.Expect)
	case "cw":
		// MorseDecode reads from a file; the temp WAV must not survive the
		// decode or a fleet of CW targets leaks one file per clip.
		if tmp, err := writeTempWAV16(samples16, rate); err == nil {
			if morse, derr := MorseDecode(tmp); derr == nil {
				result.Text = morse.Text
			}
			_ = os.Remove(tmp)
		}
		result.Gate = verify.CW(result.Text, target.Expect)
	case "navtex":
		msgs := NAVTEXDecode(samples, rate, 0)
		for _, m := range msgs {
			result.Text += m.Raw + "\n"
		}
		result.Gate = verify.Text("navtex", result.Text, target.Expect)
	case "wefax":
		if pngData, err := WEFAXImage(samples, rate, target.LinesPerMin, target.IOC); err == nil {
			result.ImagePNG = pngData
			result.Gate = verify.Image("wefax", pngData)
		} else {
			result.Gate = verify.Result{Live: false, Reason: err.Error()}
		}
	case "ft8":
		proto := "FT8"
		if strings.Contains(strings.ToUpper(target.Name), "FT4") {
			proto = "FT4"
		}
		start := float64(result.Started.UnixNano())/1e9 - float64(len(samples))/float64(rate)
		var lines []string
		for _, m := range FTXDecodeStream(samples, rate, start, proto) {
			lines = append(lines, fmt.Sprintf("%s %s %+.1f %.0f Hz %s", time.Unix(m.SlotUnix, 0).UTC().Format("150405"), m.Protocol, m.DT, m.FreqHz, m.Text))
		}
		result.Text = strings.Join(lines, "\n")
		result.Gate = verify.FTX(len(lines), proto == "FT8", result.Text)
	case "wspr":
		start := float64(result.Started.UnixNano())/1e9 - float64(len(samples))/float64(rate)
		var lines []string
		for _, w := range WSPRWindows(samples, rate, start) {
			for _, m := range w.Messages {
				lines = append(lines, fmt.Sprintf("%s WSPR %+.0f %+.1f %.1f Hz %s", time.Unix(w.Start, 0).UTC().Format("1504"), m.SNR, m.DT, m.FreqHz, m.Text))
			}
		}
		result.Text = strings.Join(lines, "\n")
		result.Gate = verify.WSPR(len(lines), result.Text)
	default:
		result.Gate = verify.Result{Live: false, Reason: "no backend for kind " + target.Kind}
	}
	return result
}

// WriteWAV16 renders mono 16-bit PCM as a RIFF/WAVE file body.
func WriteWAV16(samples []int16, rate int) []byte {
	data := len(samples) * 2
	buf := make([]byte, 44+data)
	copy(buf[0:4], "RIFF")
	putU32(buf[4:], uint32(36+data))
	copy(buf[8:12], "WAVE")
	copy(buf[12:16], "fmt ")
	putU32(buf[16:], 16)
	putU16(buf[20:], 1)
	putU16(buf[22:], 1)
	putU32(buf[24:], uint32(rate))
	putU32(buf[28:], uint32(rate*2))
	putU16(buf[32:], 2)
	putU16(buf[34:], 16)
	copy(buf[36:40], "data")
	putU32(buf[40:], uint32(data))
	for i, s := range samples {
		putU16(buf[44+i*2:], uint16(s))
	}
	return buf
}

func putU32(b []byte, v uint32) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
	b[2] = byte(v >> 16)
	b[3] = byte(v >> 24)
}
func putU16(b []byte, v uint16) { b[0] = byte(v); b[1] = byte(v >> 8) }

// writeTempWAV16 saves samples for decoders that read from a file.
func writeTempWAV16(samples []int16, rate int) (string, error) {
	f, err := createTempWAV()
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.Write(WriteWAV16(samples, rate)); err != nil {
		return "", err
	}
	return f.Name(), nil
}

// SNREstimate measures tone-over-noise for a capture health check before
// committing a listen slot to a decode.
func SNREstimate(samples []float64, rate int, toneHz float64) float64 {
	tone := goertzel(samples, rate, toneHz)
	var floor []float64
	for df := -600.0; df <= 600; df += 200 {
		if math.Abs(df) < 150 {
			continue
		}
		floor = append(floor, goertzel(samples, rate, toneHz+df))
	}
	if len(floor) == 0 {
		return 0
	}
	sum := 0.0
	for _, f := range floor {
		sum += f
	}
	return tone / (sum/float64(len(floor)) + 1e-12)
}
