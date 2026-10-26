package iqsrc

// UberSDR raw-IQ source. UberSDR (github.com/madpsy/ka9q_ubersdr, GPL-3.0) serves
// wide IQ (iq48/iq96/iq192/iq384) over a websocket in its "PCM4" predictive
// format. The protocol work lives in internal/ubersdr; this file gives it the
// same client shape as the other sources here.
//
// Nothing here assumes a mode is available: the /connection precheck says which
// wide modes this session may use, and the server's own status/IQ frames say
// what rate it actually sends. An operator that publishes no public wide mode
// gives a clear error, not a retry loop. A 429 is returned as ErrRateLimited
// and never retried here.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/common"
	"github.com/chaim12345/sdrstream/go/internal/ubersdr"
)

// ErrUberRateLimited is the server refusing this address for opening too many
// sessions. It lifts on its own; do not retry in a loop.
var ErrUberRateLimited = ubersdr.ErrRateLimited

// UberConfig selects an UberSDR receiver and a wide IQ rate.
type UberConfig struct {
	URL         string // http(s)://host[:port]; no path
	Password    string // normally empty: public modes only. Never logged.
	Frequency   uint32 // Hz, channel centre
	SampleRate  uint32 // Hz, desired; the largest allowed mode <= this, else the smallest allowed
	DialTimeout time.Duration
	// AllowPrivate permits loopback/private hosts (own receiver, LAN,
	// tailscale). When false every connection goes through the public-only
	// dial guard and redirects are refused.
	AllowPrivate bool
	// InsecureTLS skips certificate verification (self-signed receivers).
	InsecureTLS bool
}

// ErrUberSessionTime is returned once the server's announced session limit has
// elapsed; the caller reconnects (with a fresh session) if it wants more.
var ErrUberSessionTime = errors.New("ubersdr: server session time limit reached")

// UberClient is one UberSDR IQ session.
type UberClient struct {
	srv       *ubersdr.Server
	conn      *ubersdr.IQConn
	khz       int
	mu        sync.Mutex
	rate      uint32
	maxTime   time.Duration
	started   time.Time
	pending   []complex64
	bad       uint64
	closeOnce sync.Once
}

// DialUber runs the precheck, opens the socket and waits for the first frame so
// the true sample rate is known.
func DialUber(ctx context.Context, cfg UberConfig) (*UberClient, error) {
	timeout := cfg.DialTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	srv, err := ubersdr.NewServer(cfg.URL, cfg.Password)
	if err != nil {
		return nil, err
	}
	srv.InsecureTLS = cfg.InsecureTLS
	if cfg.AllowPrivate {
		d := &net.Dialer{Timeout: timeout}
		srv.NetDialContext = d.DialContext
	} else {
		if err := common.CheckPublicHost(ctx, srv.Host()); err != nil {
			return nil, err
		}
		srv.NetDialContext = common.PublicDialContext(timeout)
	}
	dctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	id := ubersdr.NewSessionID() // fresh per connect: the server voids it on close
	res, err := srv.Check(dctx, id)
	if err != nil {
		return nil, err
	}
	if res.RateLimited() {
		return nil, ErrUberRateLimited
	}
	if !res.Allowed {
		return nil, fmt.Errorf("ubersdr: connection refused: %s", res.Reason)
	}
	khz, err := pickUberKHz(res, cfg.SampleRate)
	if err != nil {
		return nil, err
	}
	conn, err := srv.DialIQ(dctx, id, int64(cfg.Frequency), khz, 0)
	if err != nil {
		return nil, err
	}
	c := &UberClient{srv: srv, conn: conn, khz: khz, rate: uint32(khz) * 1000,
		maxTime: time.Duration(res.MaxSessionTime) * time.Second, started: time.Now()}
	// First status or IQ frame gives the rate the server really uses.
	for {
		m, err := conn.ReadIdle(timeout)
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("ubersdr: waiting for first frame: %w", err)
		}
		switch m.Kind {
		case ubersdr.MsgStatus:
			if m.SampleRate > 0 {
				c.rate = uint32(m.SampleRate)
			}
			return c, nil
		case ubersdr.MsgIQ:
			if m.SampleRate <= 0 {
				continue
			}
			c.rate = uint32(m.SampleRate)
			c.pending = toComplex(m.Samples)
			return c, nil
		case ubersdr.MsgError:
			conn.Close()
			return nil, fmt.Errorf("ubersdr: server error: %s", m.Text)
		}
	}
}

// pickUberKHz chooses among the modes this session may use. With no wide mode
// allowed it fails; with an unknown list (old server) it trusts the request.
func pickUberKHz(res ubersdr.ConnResult, want uint32) (int, error) {
	var allowed []int
	for _, m := range ubersdr.WideIQModes {
		if k := ubersdr.KHzForMode(m); k > 0 && res.AllowsKHz(k) {
			allowed = append(allowed, k)
		}
	}
	if res.ModesKnown && len(allowed) == 0 {
		return 0, errors.New("ubersdr: receiver offers no wide IQ mode to this session (public_iq_modes empty)")
	}
	best := 0
	for _, k := range allowed { // rate order
		if uint32(k)*1000 <= want {
			best = k
		}
	}
	if best == 0 {
		best = allowed[0]
	}
	return best, nil
}

func toComplex(s []int16) []complex64 {
	out := make([]complex64, len(s)/2)
	for i := range out {
		out[i] = complex(float32(s[2*i])/32768, float32(s[2*i+1])/32768)
	}
	return out
}

// SampleRate is the rate the server reported, in Hz.
func (c *UberClient) SampleRate() uint32 { c.mu.Lock(); defer c.mu.Unlock(); return c.rate }

// MaxSessionTime is the limit the server announced, 0 for none.
func (c *UberClient) MaxSessionTime() time.Duration { return c.maxTime }

// BadFrames counts binary frames that did not decode.
func (c *UberClient) BadFrames() uint64 { c.mu.Lock(); defer c.mu.Unlock(); return c.bad }

// ReadSamples fills buf with normalized IQ (int16/32768); a frame larger than
// buf is held for the next call. Server errors end the stream.
func (c *UberClient) ReadSamples(buf []complex64) (int, error) {
	if len(buf) == 0 {
		return 0, nil
	}
	for len(c.pending) == 0 {
		idle := 15 * time.Second
		if c.maxTime > 0 {
			left := c.maxTime - time.Since(c.started)
			if left <= 0 {
				return 0, ErrUberSessionTime
			}
			if left < idle {
				idle = left
			}
		}
		m, err := c.conn.ReadIdle(idle)
		if err != nil {
			if c.maxTime > 0 && time.Since(c.started) >= c.maxTime {
				return 0, ErrUberSessionTime
			}
			return 0, err
		}
		switch m.Kind {
		case ubersdr.MsgIQ:
			if m.SampleRate <= 0 {
				c.mu.Lock()
				c.bad++
				c.mu.Unlock()
				continue
			}
			c.mu.Lock()
			c.rate = uint32(m.SampleRate)
			c.mu.Unlock()
			c.pending = toComplex(m.Samples)
		case ubersdr.MsgStatus:
			if m.SampleRate > 0 {
				c.mu.Lock()
				c.rate = uint32(m.SampleRate)
				c.mu.Unlock()
			}
		case ubersdr.MsgError:
			return 0, fmt.Errorf("ubersdr: server error: %s", m.Text)
		case ubersdr.MsgBad:
			c.mu.Lock()
			c.bad++
			c.mu.Unlock()
		}
	}
	if c.maxTime > 0 && time.Since(c.started) >= c.maxTime {
		return 0, ErrUberSessionTime
	}
	n := copy(buf, c.pending)
	c.pending = c.pending[n:]
	return n, nil
}

// SetFrequency retunes in place, keeping the session's mode.
func (c *UberClient) SetFrequency(hz uint32) error {
	return c.conn.Tune(int64(hz), c.khz)
}

// Close ends the session; safe to call more than once.
func (c *UberClient) Close() error {
	var err error
	c.closeOnce.Do(func() { err = c.conn.Close() })
	return err
}
