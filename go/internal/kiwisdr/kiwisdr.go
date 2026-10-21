// Package kiwisdr connects to KiwiSDR receivers over their WebSocket
// protocol: a /SND stream that speaks short "SET name=value" commands in text
// frames and binary frames tagged with a three byte marker.
//
// The handshake is easy to get wrong, so it is spelled out here the way the
// server performs it (jks-prv/KiwiSDR net/net.cpp): authenticate with
// "SET auth t=kiwi p=", agree the audio rate with "SET AR OK in=12000
// out=44100", tune with "SET mod=usb low_cut=high_cut=freq=", and only then
// do SND frames start to flow. Without the AR OK line the server answers the
// config messages and then goes forever silent.
package kiwisdr

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// SND frame flags (client.py SND_FLAG_*).
const (
	flagADCOverrun  = 0x02
	flagStereo      = 0x08
	flagCompressed  = 0x10
	flagLittleEndLs = 0x80
)

// Passbands the browser client uses per mode, in Hz around the tuned centre.
var DefaultPassbands = map[string][2]int{
	"am":   {-4900, 4900},
	"amn":  {-2500, 2500},
	"amw":  {-6000, 6000},
	"lsb":  {-2700, -300},
	"usb":  {300, 2700},
	"cw":   {300, 700},
	"cwn":  {470, 530},
	"nbfm": {-6000, 6000},
	"iq":   {-5000, 5000},
}

// Options controls reconnection and identity.
type Options struct {
	Reconnect    bool
	Password     string
	IdentUser    string
	OpenTimeout  time.Duration
	IdleTimeout  time.Duration
	Waterfall    bool // also open the /W/F waterfall connection
	WriteTimeout time.Duration
}

// DefaultOptions are the values the CLI is happy with.
func DefaultOptions() Options {
	return Options{
		Reconnect:   true,
		IdentUser:   "sdrstream",
		OpenTimeout: 15 * time.Second,
		IdleTimeout: 30 * time.Second,
	}
}

// Chunk is one block of demodulated audio. SampleRate is rounded from the
// server's float (11998.947... on most Kiwis).
type Chunk struct {
	Samples    []int16
	SampleRate int
	Sequence   uint32
	Compressed bool
	Overrun    bool
	SMeterDBM  float64
}

// Info reports what the server told us about itself.
type Info struct {
	Version      string
	RXChannels   int
	BandwidthKHz float64
	GPSLat       float64
	GPSLon       float64
	RSSI         float64
}

// Client is one live KiwiSDR connection.
type Client struct {
	base    string // host[:port]
	opts    Options
	ws      *websocket.Conn
	adpcm   adpcmState
	rate    float64
	info    Info
	curFreq int64
	curMode string
}

// New parses "host:port" or any http(s)/ws(s) URL the directory lists.
func New(server string, opts Options) (*Client, error) {
	base := server
	if strings.Contains(server, "://") {
		u, err := url.Parse(server)
		if err != nil {
			return nil, err
		}
		base = u.Host
	}
	if base == "" {
		return nil, fmt.Errorf("kiwisdr: empty host")
	}
	if opts.IdleTimeout == 0 {
		opts.IdleTimeout = 30 * time.Second
	}
	return &Client{base: base, opts: opts, rate: 12000}, nil
}

// Connect opens the stream and starts audio flowing. It does not tune; call
// Tune separately so repeated reconnections replay the last tuning.
func (c *Client) Connect(ctx context.Context) error {
	uri := fmt.Sprintf("ws://%s/%d/SND", c.base, time.Now().Unix())
	dialer := websocket.Dialer{HandshakeTimeout: c.opts.OpenTimeout}
	ws, _, err := dialer.DialContext(ctx, uri, nil)
	if err != nil {
		return fmt.Errorf("kiwisdr: %s: %w", c.base, err)
	}
	c.ws = ws
	c.adpcm = adpcmState{}
	write := func(line string) error {
		_ = ws.SetWriteDeadline(time.Now().Add(writeDeadline(c)))
		return ws.WriteMessage(websocket.TextMessage, []byte(line))
	}
	if err := write("SET auth t=kiwi p=" + c.opts.Password); err != nil {
		return err
	}
	if err := write("SET AR OK in=12000 out=44100"); err != nil {
		return err
	}
	if err := write("SET agc=1 hang=0 thresh=-100 slope=6 decay=1000 manGain=50"); err != nil {
		return err
	}
	if err := write("SET ident_user=" + c.opts.IdentUser); err != nil {
		return err
	}
	if c.curFreq != 0 {
		return c.tuneSend(c.curFreq, c.curMode)
	}
	return nil
}

func writeDeadline(c *Client) time.Duration {
	if c.opts.WriteTimeout != 0 {
		return c.opts.WriteTimeout
	}
	return 10 * time.Second
}

// Tune to a downlink given in Hertz. The server is told kHz.
func (c *Client) Tune(ctx context.Context, freqHz int64, mode string) error {
	if mode == "" {
		mode = "usb"
	}
	mode = strings.ToLower(mode)
	if _, known := DefaultPassbands[mode]; !known {
		return fmt.Errorf("kiwisdr: unknown mode %q; known: %s", mode, knownModes())
	}
	if mode == "am" {
		// AM tuning keeps the carrier centred: wide symmetric passband.
	}
	c.curFreq, c.curMode = freqHz, mode
	return c.tuneSend(freqHz, mode)
}

func (c *Client) tuneSend(freqHz int64, mode string) error {
	band := DefaultPassbands[mode]
	freqKHz := float64(freqHz) / 1000.0
	line := fmt.Sprintf("SET mod=%s low_cut=%d high_cut=%d freq=%.3f",
		mode, band[0], band[1], freqKHz)
	c.ws.SetWriteDeadline(time.Now().Add(writeDeadline(c)))
	return c.ws.WriteMessage(websocket.TextMessage, []byte(line))
}

// Info snapshot of what the server has announced so far.
func (c *Client) InfoSnapshot() Info { return c.info }

// SampleRate is the negotiated decode rate, only valid once audio flows.
func (c *Client) SampleRate() int { return int(math.Round(c.rate)) }

// Record collects duration of audio and returns samples plus rate.
func (c *Client) Record(ctx context.Context, duration time.Duration) ([]int16, int, error) {
	if c.ws == nil {
		return nil, 0, fmt.Errorf("kiwisdr: not connected")
	}
	deadline := time.After(duration)
	keepalive := time.NewTicker(time.Second)
	defer keepalive.Stop()
	var out []int16
	rate := 12000
	for {
		select {
		case <-ctx.Done():
			return out, rate, ctx.Err()
		case <-keepalive.C:
			c.ws.SetWriteDeadline(time.Now().Add(writeDeadline(c)))
			_ = c.ws.WriteMessage(websocket.TextMessage, []byte("SET keepalive"))
		case <-deadline:
			return out, rate, nil
		default:
			c.ws.SetReadDeadline(time.Now().Add(c.opts.IdleTimeout))
			kind, data, err := c.ws.ReadMessage()
			if err != nil {
				if websocket.IsUnexpectedCloseError(err) && c.opts.Reconnect {
					time.Sleep(500 * time.Millisecond)
					if err2 := c.Connect(ctx); err2 == nil {
						continue
					}
				}
				return out, rate, err
			}
			if kind == websocket.TextMessage {
				c.handleText(string(data))
				continue
			}
			if len(data) >= 4 && string(data[:3]) == "MSG" {
				// Control packets arrive as tagged binary frames: the marker,
				// one spare byte, then "name=value name=value …" as text.
				c.handleText("MSG " + string(data[4:]))
				continue
			}
			if len(data) < 3 || string(data[:3]) != "SND" {
				continue // W/F frames belong to the waterfall connection
			}
			body := data[3:]
			if len(body) < 7 {
				continue
			}
			flags := body[0]
			seq := binary.LittleEndian.Uint32(body[1:5])
			smeter := int(binary.BigEndian.Uint16(body[5:7]))*10 - 127*10
			chunk := decodeSampleBlock(flags, body[7:], &c.adpcm)
			if len(chunk.Samples) > 0 {
				if rate == 12000 {
					rate = int(math.Round(c.rate))
				}
				chunk.Sequence = seq
				chunk.SMeterDBM = float64(smeter) / 10
				out = append(out, chunk.Samples...)
			}
		}
	}
}

// Close shuts the socket down.
func (c *Client) Close() error {
	if c.ws == nil {
		return nil
	}
	err := c.ws.Close()
	c.ws = nil
	return err
}

func (c *Client) handleText(text string) {
	if !strings.HasPrefix(text, "MSG ") {
		return
	}
	body := text[4:]
	for _, pair := range strings.Fields(body) {
		name, value, found := strings.Cut(pair, "=")
		if !found {
			continue
		}
		switch name {
		case "sample_rate":
			if f, err := strconv.ParseFloat(value, 64); err == nil {
				c.rate = f
			}
		case "bandwidth":
			if f, err := strconv.ParseFloat(value, 64); err == nil {
				c.info.BandwidthKHz = f
			}
		case "version_maj", "version_min":
			// version messages land as separate pairs; keep both and join later.
		case "rx_chans":
			if n, err := strconv.Atoi(value); err == nil {
				c.info.RXChannels = n
			}
		}
	}
	// The version arrives as "version_maj=1 version_min=902" on one line; the
	// loose scan above would lose it, so re-scan for the pair explicitly.
	if i := strings.Index(body, "version_maj="); i >= 0 {
		if j := strings.Index(body[i:], "version_min="); j > i {
			maj := strings.TrimSpace(body[i+len("version_maj="):])
			if k := strings.Index(maj, " "); k >= 0 {
				maj = maj[:k]
			}
			min := strings.TrimSpace(body[i+j+len("version_min="):])
			if k := strings.Index(min, " "); k >= 0 {
				min = min[:k]
			}
			c.info.Version = maj + "." + min
		}
	}
	if value, ok := extractURLEncoded(body, "load_cfg"); ok {
		parseRxGPS(value, &c.info)
	}
}
