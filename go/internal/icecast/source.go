package icecast

import (
	"context"
	"time"
)

// Source adapts one stream to the monitor's receiver interface. A stream has
// no spectrum and no tuning, so Spectrum reports none and Listen ignores the
// frequency and mode and records the next dur of audio.
type Source struct {
	URL    string
	Client *Client
}

// NewSource returns a Source for url.
func NewSource(url string) *Source { return &Source{URL: url, Client: NewClient()} }

// Spectrum implements the monitor.Source interface.
func (s *Source) Spectrum() ([]float64, float64, float64, bool) { return nil, 0, 0, false }

// Listen implements the monitor.Source interface.
func (s *Source) Listen(ctx context.Context, _ float64, _ string, dur time.Duration) ([]float64, int, error) {
	c, err := s.Client.Capture(ctx, s.URL, dur)
	if err != nil {
		return nil, 0, err
	}
	return c.Samples, PCMRate, nil
}

// Close implements the monitor.Source interface.
func (s *Source) Close() error { return nil }
