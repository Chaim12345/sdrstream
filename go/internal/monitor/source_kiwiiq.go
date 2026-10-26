package monitor

import (
	"context"
	"fmt"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/iqdemod"
	"github.com/chaim12345/sdrstream/go/internal/iqsrc"
)

// kiwiIQSource watches a KiwiSDR receiver in its IQ mode. The stream is raw
// IQ decoded with the owning-source 20-byte header parser (iqsrc); iqdemod
// turns the tuned channel into audio, so the decoder fleet treats it like
// any other audio source. The sample rate is the server's own announced
// rate, never assumed.
type kiwiIQSource struct {
	client *iqsrc.KiwiIQClient
	name   string
}

// dialKiwiIQSource connects to a KiwiSDR (host:port) for IQ-mode listening.
func dialKiwiIQSource(ctx context.Context, server string, allowPrivate bool) (Source, error) {
	client, err := iqsrc.DialKiwiIQ(ctx, iqsrc.KiwiIQConfig{
		Server: server, AllowPrivate: allowPrivate, IdentUser: "sdrstream",
	})
	if err != nil {
		return nil, err
	}
	return &kiwiIQSource{client: client, name: server}, nil
}

// Spectrum reports none: the IQ-mode stream carries no waterfall.
func (s *kiwiIQSource) Spectrum() ([]float64, float64, float64, bool) {
	return nil, 0, 0, false
}

// Listen retunes the channel and records dur of demodulated audio.
func (s *kiwiIQSource) Listen(ctx context.Context, hz float64, mode string, dur time.Duration) ([]float64, int, error) {
	dm, err := spyMode(mode)
	if err != nil {
		return nil, 0, err
	}
	if err := s.client.SetFrequency(int64(hz)); err != nil {
		return nil, 0, err
	}
	select {
	case <-ctx.Done():
		return nil, 0, ctx.Err()
	case <-time.After(300 * time.Millisecond): // tuner settle, mirrors kiwiSource
	}
	inRate, err := s.client.WaitRate(3 * time.Second)
	if err != nil {
		return nil, 0, fmt.Errorf("kiwi-iq: %w", err)
	}
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
			return nil, 0, fmt.Errorf("kiwi-iq: capture stalled after %v", dur)
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
func (s *kiwiIQSource) Close() error { return s.client.Close() }
