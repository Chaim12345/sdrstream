package monitor

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/iqdemod"
	"github.com/chaim12345/sdrstream/go/internal/iqsrc"
)

// spySource watches a remote SDR over the Airspy SpyServer protocol. The
// server delivers raw IQ; iqdemod turns the tuned channel into audio, so
// any sdrstream decoder that works on KiwiSDR/WebSDR audio works here.
type spySource struct {
	client *iqsrc.SpyClient
	name   string
}

// SpyServerSource connects to a SpyServer (host:port). addr may carry a
// spyserver:// prefix; it is stripped.
func SpyServerSource(ctx context.Context, addr string, frequency uint32, sampleRate uint32, gain uint32) (Source, error) {
	addr = strings.TrimPrefix(addr, "spyserver://")
	addr = strings.TrimPrefix(addr, "sdr://")
	client, err := iqsrc.DialSpy(ctx, iqsrc.SpyConfig{
		Addr: addr, Frequency: frequency, SampleRate: sampleRate,
		Gain: gain, DigitalGain: true, AppName: "sdrstream",
	})
	if err != nil {
		return nil, err
	}
	return &spySource{client: client, name: addr}, nil
}

// Spectrum reports none: we negotiate the IQ-only stream, so there is no
// FFT to scan with. (A scan-mode companion could negotiate FFT separately.)
func (s *spySource) Spectrum() ([]float64, float64, float64, bool) {
	return nil, 0, 0, false
}

// spyMode maps the pipeline's mode strings onto iqdemod modes.
func spyMode(mode string) (iqdemod.Mode, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "usb":
		return iqdemod.USB, nil
	case "lsb":
		return iqdemod.LSB, nil
	case "am":
		return iqdemod.AM, nil
	case "nbfm", "fm":
		return iqdemod.NBFM, nil
	case "cw":
		return iqdemod.USB, nil // CW is copied as a tone in a sideband
	default:
		return iqdemod.AM, fmt.Errorf("spyserver: unsupported mode %q", mode)
	}
}

// Listen retunes the channel and records dur of demodulated audio.
func (s *spySource) Listen(ctx context.Context, hz float64, mode string, dur time.Duration) ([]float64, int, error) {
	dm, err := spyMode(mode)
	if err != nil {
		return nil, 0, err
	}
	if err := s.client.SetFrequency(uint32(hz)); err != nil {
		return nil, 0, err
	}
	select {
	case <-ctx.Done():
		return nil, 0, ctx.Err()
	case <-time.After(300 * time.Millisecond): // tuner settle, mirrors kiwiSource
	}
	inRate := int(s.client.SampleRate())
	outRate := 12000
	d := iqdemod.New(inRate, outRate, 0, dm)
	want := int(dur.Seconds() * float64(outRate))
	audio := make([]float64, 0, want)
	iqbuf := make([]complex64, 16384)
	deadline := time.Now().Add(dur + 10*time.Second)
	for len(audio) < want {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		if time.Now().After(deadline) {
			return nil, 0, fmt.Errorf("spyserver: capture stalled after %v", dur)
		}
		n, err := s.client.ReadSamples(iqbuf)
		if n > 0 {
			audio = d.Process(iqbuf[:n], audio)
		}
		if err != nil {
			return nil, 0, err
		}
	}
	return audio[:want], outRate, nil
}

// Close shuts the connection.
func (s *spySource) Close() error { return s.client.Close() }
