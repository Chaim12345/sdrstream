// Web-888 (RaspSDR fork) raw-IQ source. The stream protocol is the KiwiSDR
// SND family the fork kept: the transport (websocket /{ts}/SND, SET
// negotiation) and the 20-byte-header IQ parser are reused from the kiwi-iq
// path, both grounded in the fork source (RaspSDR/server @ 6445a00,
// rx/rx_sound.{h,cpp}). The fork differences this client owns:
//
//   - Passive default frequency: with no configured frequency the client
//     follows the server's OWN announced init.freq (load_cfg MSG), offset by
//     the announced freq_offset - the behavior live-verified against four
//     fork receivers by the rxprobe harness. A frequency is never invented;
//     if the server announces none the dial fails with that fact.
//   - Rate announcement: the fork sends sample_rate as a float
//     ("MSG audio_init=0 audio_rate=12000 sample_rate=12000.000000",
//     rx_sound.cpp:174), so rate parsing accepts both int and float forms.
package iqsrc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/kiwisdr"
)

// Web888Config selects a Web-888 receiver. Zero Frequency means: use the
// server's own announced default (passive rule), never an invented value.
type Web888Config struct {
	Server       string // host[:port] or http(s)://host[:port]
	Password     string
	Frequency    int64 // Hz; 0 = server's announced default
	MaxDuration  time.Duration
	AllowPrivate bool
	IdentUser    string
}

// Web888Client is one Web-888 IQ session. It embeds the kiwi-iq machinery;
// DefaultFreq records the server-announced frequency actually tuned when the
// caller asked for the passive default (0 when the caller named one).
type Web888Client struct {
	*KiwiIQClient
	DefaultFreq int64
}

// errWeb888NoDefault is returned when the passive-default rule cannot run
// because the server announced no init frequency.
var errWeb888NoDefault = fmt.Errorf("web-888: no passive default frequency announced (load_cfg init.freq not observed)")

// DialWeb888 connects, negotiates, and tunes. With Frequency==0 it reads MSG
// frames until the server's load_cfg announcement arrives, then tunes that
// default; otherwise it tunes the configured frequency. The first IQ frame
// (or the rate announcement) must arrive within 10s of the tune.
func DialWeb888(ctx context.Context, cfg Web888Config) (*Web888Client, error) {
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
	iq := &KiwiIQClient{cli: cli, started: time.Now(), maxTime: cfg.MaxDuration}
	c := &Web888Client{KiwiIQClient: iq}
	freq := cfg.Frequency
	if freq == 0 {
		f, err := c.awaitDefaultFreq(ctx, 5*time.Second)
		if err != nil {
			cli.Close()
			return nil, err
		}
		freq = f
		c.DefaultFreq = f
	}
	if err := iq.SetFrequency(freq); err != nil {
		cli.Close()
		return nil, err
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cfg.MaxDuration > 0 && time.Since(iq.started) >= cfg.MaxDuration {
			cli.Close()
			return nil, fmt.Errorf("web-888: session time limit reached before first IQ frame")
		}
		kind, data, err := cli.ReadFrame()
		if err != nil {
			cli.Close()
			return nil, fmt.Errorf("web-888: waiting for first IQ frame: %w", err)
		}
		n, err := iq.handle(kind, data)
		if err != nil {
			cli.Close()
			return nil, err
		}
		if n > 0 || iq.rate > 0 {
			return c, nil
		}
	}
	cli.Close()
	return nil, fmt.Errorf("web-888: no IQ frames within 10s of tune")
}

// awaitDefaultFreq reads MSG frames until the server's load_cfg announcement
// yields init.freq, then applies the announced freq_offset. It never invents
// a frequency: no announcement within d is an error carrying that fact.
func (c *Web888Client) awaitDefaultFreq(ctx context.Context, d time.Duration) (int64, error) {
	deadline := time.NewTimer(d)
	defer deadline.Stop()
	var initKHz, offsetKHz float64
	haveInit := false
	for !haveInit {
		// ReadFrame blocks; race it against the window so a server that
		// holds the socket without announcing still ends in the honest
		// no-default error. On timeout the caller closes the connection,
		// which unblocks the abandoned read.
		type frame struct {
			kind int
			data []byte
			err  error
		}
		ch := make(chan frame, 1)
		go func() {
			kind, data, err := c.cli.ReadFrame()
			ch <- frame{kind, data, err}
		}()
		var f frame
		select {
		case f = <-ch:
		case <-deadline.C:
			return 0, errWeb888NoDefault
		}
		kind, data, err := f.kind, f.data, f.err
		if err != nil {
			return 0, fmt.Errorf("web-888: reading announcements: %w", err)
		}
		text := ""
		if kind == 1 { // websocket.TextMessage
			text = string(data)
		} else if len(data) >= 4 && string(data[:3]) == "MSG" {
			text = "MSG " + string(data[4:])
		}
		if !strings.HasPrefix(text, "MSG ") {
			continue
		}
		c.noteRate(text[4:])
		for _, pair := range strings.Fields(text[4:]) {
			name, value, ok := strings.Cut(pair, "=")
			if !ok {
				continue
			}
			switch name {
			case "load_cfg":
				if un, err := url.QueryUnescape(value); err == nil {
					var cfg struct {
						Init struct {
							Freq float64 `json:"freq"`
						} `json:"init"`
					}
					if json.Unmarshal([]byte(un), &cfg) == nil && cfg.Init.Freq > 0 {
						initKHz = cfg.Init.Freq
						haveInit = true
					}
				}
			case "freq_offset":
				fmt.Sscanf(value, "%g", &offsetKHz)
			}
		}
		if haveInit {
			return int64((initKHz - offsetKHz) * 1000), nil
		}
	}
	return 0, errWeb888NoDefault
}

// parseRate accepts the fork's float form ("sample_rate=12000.000000",
// rx_sound.cpp:174) as well as the mainline integer form.
func parseRate(v string) (int, bool) {
	if r, err := strconv.Atoi(v); err == nil && r > 0 {
		return r, true
	}
	if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
		return int(f + 0.5), true
	}
	return 0, false
}
