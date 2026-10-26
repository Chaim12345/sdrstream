package iqsrc

// KiwiSDR raw-IQ source. Transport (websocket, auth, tuning, public-dial
// guard) is the existing internal/kiwisdr client; frames are decoded here
// with ParseKiwiIQFrame, which implements the owning-source 20-byte IQ
// header (rx_sound.h snd_pkt_iq_t) rather than the 10-byte real-audio
// header the audio path uses.
//
// The sample rate is never assumed: it comes from the server's
// "MSG sample_rate=" announcement. A session is bounded by MaxDuration when
// the caller sets it (public batches pin this to their contact budget).

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/kiwisdr"
	"github.com/gorilla/websocket"
)

// KiwiIQConfig selects a KiwiSDR receiver and channel frequency.
type KiwiIQConfig struct {
	Server       string // host[:port]
	Frequency    int64  // Hz, channel centre
	Password     string // normally empty: public receivers only
	AllowPrivate bool   // loopback/LAN receivers and tests
	MaxDuration  time.Duration
	IdentUser    string
}

// KiwiIQClient is one KiwiSDR IQ session.
type KiwiIQClient struct {
	cli     *kiwisdr.Client
	mu      sync.Mutex
	rate    int
	started time.Time
	maxTime time.Duration
	pending []complex64
	bad     uint64
}

// DialKiwiIQ connects, tunes the IQ mode and waits for the first IQ frame
// (or the server's sample-rate announcement) so the true rate is known.
func DialKiwiIQ(ctx context.Context, cfg KiwiIQConfig) (*KiwiIQClient, error) {
	opts := kiwisdr.DefaultOptions()
	opts.Password = cfg.Password
	opts.AllowPrivate = cfg.AllowPrivate
	if cfg.IdentUser != "" {
		opts.IdentUser = cfg.IdentUser
	}
	cli, err := kiwisdr.New(cfg.Server, opts)
	if err != nil {
		return nil, err
	}
	if err := cli.Connect(ctx); err != nil {
		return nil, err
	}
	c := &KiwiIQClient{cli: cli, started: time.Now(), maxTime: cfg.MaxDuration}
	if cfg.Frequency == 0 {
		// Monitor-style use: the first SetFrequency tunes. The rate is
		// learned from MSG frames as they arrive.
		return c, nil
	}
	if err := c.SetFrequency(cfg.Frequency); err != nil {
		cli.Close()
		return nil, err
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if c.maxTime > 0 && time.Since(c.started) >= c.maxTime {
			cli.Close()
			return nil, fmt.Errorf("kiwisdr: session time limit reached before first IQ frame")
		}
		kind, data, err := cli.ReadFrame()
		if err != nil {
			cli.Close()
			return nil, fmt.Errorf("kiwisdr: waiting for first IQ frame: %w", err)
		}
		n, err := c.handle(kind, data)
		if err != nil {
			cli.Close()
			return nil, err
		}
		if n > 0 || c.rate > 0 {
			return c, nil
		}
	}
	cli.Close()
	return nil, fmt.Errorf("kiwisdr: no IQ frames within 10s of tune")
}

// handle classifies one raw frame; IQ samples are queued in pending.
func (c *KiwiIQClient) handle(kind int, data []byte) (int, error) {
	if kind == websocket.TextMessage {
		c.noteRate(string(data))
		return 0, nil
	}
	if len(data) >= 4 && string(data[:3]) == "MSG" {
		c.noteRate(string(data[4:]))
		return 0, nil
	}
	if len(data) < 3 || string(data[:3]) != "SND" {
		return 0, nil // waterfall frames belong to another connection
	}
	buf := make([]complex64, (len(data)-20)/4+1)
	_, n, err := ParseKiwiIQFrame(data, buf)
	if err != nil {
		c.mu.Lock()
		c.bad++
		c.mu.Unlock()
		return 0, nil
	}
	c.pending = append(c.pending, buf[:n]...)
	return n, nil
}

// noteRate picks the server-announced sample rate out of MSG text.
func (c *KiwiIQClient) noteRate(text string) {
	for _, field := range strings.Fields(text) {
		if v, ok := strings.CutPrefix(field, "sample_rate="); ok {
			if r, err := strconv.Atoi(v); err == nil && r > 0 {
				c.mu.Lock()
				c.rate = r
				c.mu.Unlock()
			}
		}
	}
}

// WaitRate reads frames until the server's sample-rate announcement
// arrives or d elapses. Needed when the dial deferred tuning: the MSG
// carrying the rate may not have been read yet.
func (c *KiwiIQClient) WaitRate(d time.Duration) (int, error) {
	deadline := time.Now().Add(d)
	for c.SampleRate() == 0 {
		if !time.Now().Before(deadline) {
			return 0, fmt.Errorf("kiwisdr: no sample-rate announcement within %v", d)
		}
		kind, data, err := c.cli.ReadFrame()
		if err != nil {
			return 0, err
		}
		if _, err := c.handle(kind, data); err != nil {
			return 0, err
		}
	}
	return c.SampleRate(), nil
}

// SampleRate is the rate the server announced, 0 until known.
func (c *KiwiIQClient) SampleRate() int { c.mu.Lock(); defer c.mu.Unlock(); return c.rate }

// BadFrames counts SND frames that did not decode as IQ.
func (c *KiwiIQClient) BadFrames() uint64 { c.mu.Lock(); defer c.mu.Unlock(); return c.bad }

// ReadSamples fills buf with normalized IQ (int16/32768). It returns an
// error once MaxDuration has elapsed so a caller cannot overrun its budget.
func (c *KiwiIQClient) ReadSamples(buf []complex64) (int, error) {
	if len(buf) == 0 {
		return 0, nil
	}
	for len(c.pending) == 0 {
		if c.maxTime > 0 && time.Since(c.started) >= c.maxTime {
			return 0, fmt.Errorf("kiwisdr: session time limit reached")
		}
		kind, data, err := c.cli.ReadFrame()
		if err != nil {
			return 0, err
		}
		if _, err := c.handle(kind, data); err != nil {
			return 0, err
		}
	}
	n := copy(buf, c.pending)
	c.pending = c.pending[n:]
	return n, nil
}

// SetFrequency retunes the session's channel, keeping IQ mode.
func (c *KiwiIQClient) SetFrequency(hz int64) error {
	return c.cli.Tune(context.Background(), hz, "iq")
}

// Close ends the session; safe to call more than once.
func (c *KiwiIQClient) Close() error { return c.cli.Close() }
