package decode

import (
	"context"
	"fmt"
	"math"
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
	result := HFResult{Target: target, Server: server, Started: time.Now().UTC()}
	opts := kiwisdr.DefaultOptions()
	opts.OpenTimeout = 10 * time.Second
	opts.IdleTimeout = 13 * time.Second
	client, err := kiwisdr.New(server, opts)
	if err != nil {
		return result, err
	}
	defer client.Close()
	if err := client.Connect(ctx); err != nil {
		return result, fmt.Errorf("connect %s: %w", server, err)
	}
	if err := client.Tune(ctx, int64(target.DialKHz*1000), target.Mode); err != nil {
		return result, fmt.Errorf("tune: %w", err)
	}
	samples16, rate, err := client.Record(ctx, listen)
	if err != nil {
		return result, fmt.Errorf("record: %w", err)
	}
	samples := make([]float64, len(samples16))
	for i, s := range samples16 {
		samples[i] = float64(s) / 32768.0
	}
	result.AudioWAV = WriteWAV16(samples16, rate)

	switch target.Kind {
	case "rtty":
		text, _ := RTTYDecodeAuto(samples, rate, target.MarkHz, target.SpaceHz, target.Baud)
		result.Text = text
		result.Gate = verify.Text("rtty", text, target.Expect)
	case "cw":
		tmp, err := writeTempWAV16(samples16, rate)
		if err != nil {
			return result, err
		}
		morse, err := MorseDecode(tmp)
		if err == nil {
			result.Text = morse.Text
		}
		result.Gate = verify.CW(result.Text, target.Expect)
	case "navtex":
		msgs := NAVTEXDecode(samples, rate, (target.MarkHz+target.SpaceHz)/2)
		for _, m := range msgs {
			result.Text += m.Raw + "\n"
		}
		result.Gate = verify.Text("navtex", result.Text, target.Expect)
	case "wefax":
		pngData, err := WEFAXImage(samples, rate, target.LinesPerMin, target.IOC)
		if err != nil {
			return result, err
		}
		result.ImagePNG = pngData
		result.Gate = verify.Image("wefax", pngData)
	default:
		return result, fmt.Errorf("unsupported target kind %q", target.Kind)
	}
	return result, nil
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
