package monitor

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/iqdemod"
	"github.com/chaim12345/sdrstream/go/internal/iqsrc"
)

// uberSource listens on an UberSDR receiver's wideband IQ stream (PCM4 over
// websocket). iqdemod turns the tuned channel into audio, so the decoder
// fleet treats it like any other audio source. The wideband mode and true
// sample rate are server-announced (iqsrc picks the largest allowed mode the
// request fits), never assumed. Public endpoints use the public-only dial
// guard; private hosts need allowPrivate.
type uberSource struct {
	client *iqsrc.UberClient
	name   string
}

// dialUberSource connects to an UberSDR receiver (URL or host:port) for
// wideband IQ listening.
func dialUberSource(ctx context.Context, server string, allowPrivate bool) (Source, error) {
	url := server
	if !strings.Contains(url, "://") {
		url = "http://" + url
	}
	client, err := iqsrc.DialUber(ctx, iqsrc.UberConfig{
		URL: url, AllowPrivate: allowPrivate,
	})
	if err != nil {
		return nil, err
	}
	return &uberSource{client: client, name: server}, nil
}

// Spectrum reports none: the stream carries no waterfall.
func (s *uberSource) Spectrum() ([]float64, float64, float64, bool) {
	return nil, 0, 0, false
}

// Listen retunes the channel and records dur of demodulated audio. Reaching
// the server's announced session limit ends the capture (ErrUberSessionTime);
// the session is one-shot by server policy and the caller reconnects for more.
func (s *uberSource) Listen(ctx context.Context, hz float64, mode string, dur time.Duration) ([]float64, int, error) {
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
	case <-time.After(300 * time.Millisecond): // tuner settle, mirrors kiwi-iq
	}
	inRate := int(s.client.SampleRate())
	if inRate <= 0 {
		return nil, 0, fmt.Errorf("ubersdr: no negotiated sample rate")
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
			return nil, 0, fmt.Errorf("ubersdr: capture stalled after %v", dur)
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

// Close shuts the session.
func (s *uberSource) Close() error { return s.client.Close() }
