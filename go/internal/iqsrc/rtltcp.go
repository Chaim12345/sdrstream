// Package iqsrc connects sdrstream to remote IQ sources over IP. This file
// implements an rtl_tcp client: the simplest wideband remote-SDR protocol
// (12-byte header, 5-byte commands, raw interleaved unsigned uint8 I/Q).
// Pure Go, no cgo - safe for the Android gomobile build.
package iqsrc

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// rtl_tcp command opcodes (1 byte command + 4 byte big-endian param).
const (
	cmdSetFreq       = 0x01
	cmdSetSampleRate = 0x02
	cmdSetGainMode   = 0x03 // 0=auto, 1=manual
	cmdSetGain       = 0x04 // tenths of dB
	cmdSetAGC        = 0x08 // 0=off, 1=on
)

// Tuner type values from the header (informational).
const (
	TunerUnknown   = 0
	TunerE4000     = 1
	TunerFC0012    = 2
	TunerFC0013    = 3
	TunerFC2580    = 4
	TunerR820T     = 5
	TunerR828D     = 6
	TunerR828Tuner = TunerR828D
)

// Config selects a remote server and the radio parameters to program.
type Config struct {
	Addr       string // host:port
	Frequency  uint32 // Hz
	SampleRate uint32 // Hz
	ManualGain bool   // true: program GainTenthsDB; false: auto gain
	GainTenthsDB uint32
	AGC        bool
	DialTimeout time.Duration // 0 -> 10s
}

// Client is one rtl_tcp connection. Only the read side is used after Dial
// applies Config; SetFrequency supports live retune.
type Client struct {
	conn       net.Conn
	tunerType  uint32
	tunerGains uint32 // count of gain steps the tuner reported
	mu         sync.Mutex
	closed     bool
}

// Dial connects, reads the 12-byte header, and programs the radio.
func Dial(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.Addr == "" {
		return nil, errors.New("iqsrc: empty address")
	}
	to := cfg.DialTimeout
	if to == 0 {
		to = 10 * time.Second
	}
	d := net.Dialer{Timeout: to}
	conn, err := d.DialContext(ctx, "tcp", cfg.Addr)
	if err != nil {
		return nil, fmt.Errorf("iqsrc: dial %s: %w", cfg.Addr, err)
	}
	c := &Client{conn: conn}
	var hdr [12]byte
	if _, err := io.ReadFull(conn, hdr[:]); err != nil {
		conn.Close()
		return nil, fmt.Errorf("iqsrc: header: %w", err)
	}
	if string(hdr[0:4]) != "RTL0" {
		conn.Close()
		return nil, fmt.Errorf("iqsrc: bad magic %q - not an rtl_tcp server", hdr[0:4])
	}
	c.tunerType = binary.BigEndian.Uint32(hdr[4:8])
	c.tunerGains = binary.BigEndian.Uint32(hdr[8:12])

	type setting struct {
		cmd   byte
		param uint32
	}
	gainMode := uint32(0)
	if cfg.ManualGain {
		gainMode = 1
	}
	agc := uint32(0)
	if cfg.AGC {
		agc = 1
	}
	settings := []setting{
		{cmdSetGainMode, gainMode},
		{cmdSetAGC, agc},
	}
	if cfg.ManualGain {
		settings = append(settings, setting{cmdSetGain, cfg.GainTenthsDB})
	}
	settings = append(settings,
		setting{cmdSetSampleRate, cfg.SampleRate},
		setting{cmdSetFreq, cfg.Frequency},
	)
	for _, s := range settings {
		if err := c.command(s.cmd, s.param); err != nil {
			conn.Close()
			return nil, err
		}
	}
	return c, nil
}

// TunerType and TunerGainCount report header fields.
func (c *Client) TunerType() uint32      { return c.tunerType }
func (c *Client) TunerGainCount() uint32 { return c.tunerGains }

func (c *Client) command(cmd byte, param uint32) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("iqsrc: closed")
	}
	var b [5]byte
	b[0] = cmd
	binary.BigEndian.PutUint32(b[1:5], param)
	_, err := c.conn.Write(b[:])
	if err != nil {
		return fmt.Errorf("iqsrc: command 0x%02x: %w", cmd, err)
	}
	return nil
}

// SetFrequency retunes the center frequency on a live connection.
func (c *Client) SetFrequency(hz uint32) error {
	return c.command(cmdSetFreq, hz)
}

// ReadSamples fills buf with one block of interleaved I/Q converted to
// normalized complex64 (uint8 v -> (v-127.5)/128). It blocks until buf is
// full or the connection fails; partial reads return io.ErrUnexpectedEOF.
func (c *Client) ReadSamples(buf []complex64) (int, error) {
	raw := make([]byte, 2*len(buf))
	n, err := io.ReadFull(c.conn, raw)
	if n == 0 && err != nil {
		return 0, err
	}
	ns := n / 2
	Uint8ToComplex(buf[:ns], raw[:2*ns])
	if err != nil {
		return ns, err
	}
	return ns, nil
}

// Uint8ToComplex converts interleaved unsigned uint8 I/Q to complex64
// normalized to roughly [-1, 1). src must have even length.
func Uint8ToComplex(dst []complex64, src []byte) {
	for i := 0; i < len(dst); i++ {
		iq := (float32(src[2*i]) - 127.5) / 128
		qq := (float32(src[2*i+1]) - 127.5) / 128
		dst[i] = complex(iq, qq)
	}
}

// Close ends the connection.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	return c.conn.Close()
}
