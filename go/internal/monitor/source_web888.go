package monitor

import (
	"context"
	"fmt"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/iqdemod"
	"github.com/chaim12345/sdrstream/go/internal/iqsrc"
)

// web888Source listens on a Web-888 (RaspSDR fork) receiver's IQ stream.
// The fork speaks the KiwiSDR SND family; the client (iqsrc) owns the
// fork-verified differences: the passive default frequency rule and the
// float sample_rate announcement. The sample rate is always the server's
// own announcement, never assumed.
type web888Source struct {
	client *iqsrc.Web888Client
	name   string
}

// dialWeb888Source connects to a Web-888 receiver (host[:port] or URL).
// The monitor always tunes before listening, so the passive default is not
// used here; it exists for dial-without-tune consumers.
func dialWeb888Source(ctx context.Context, server string, allowPrivate bool) (Source, error) {
	client, err := iqsrc.DialWeb888(ctx, iqsrc.Web888Config{
		Server: server, AllowPrivate: allowPrivate, IdentUser: "sdrstream",
	})
	if err != nil {
		return nil, err
	}
	return &web888Source{client: client, name: server}, nil
}

// Spectrum reports none: the IQ stream carries no waterfall.
func (s *web888Source) Spectrum() ([]float64, float64, float64, bool) {
	return nil, 0, 0, false
}

// Listen retunes the channel and records dur of demodulated audio.
func (s *web888Source) Listen(ctx context.Context, hz float64, mode string, dur time.Duration) ([]float64, int, error) {
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
	case <-time.After(300 * time.Millisecond): // tuner settle, mirrors kiwi-iq
	}
	inRate, err := s.client.WaitRate(3 * time.Second)
	if err != nil {
		return nil, 0, fmt.Errorf("web-888: %w", err)
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
			return nil, 0, fmt.Errorf("web-888: capture stalled after %v", dur)
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
func (s *web888Source) Close() error { return s.client.Close() }
