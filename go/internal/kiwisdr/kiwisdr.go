// Package kiwisdr connects to KiwiSDR receivers over their WebSocket
// protocol: a /SND stream that speaks short "SET name=value" commands in text
// frames and binary frames tagged with a three byte marker.
//
// The client negotiates a receiver session using observed WebSocket text
// commands and binary audio frames. No external client implementation is
// incorporated.
package kiwisdr

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/chaim12345/sdrstream/go/internal/common"
)

// SND frame flag values interpreted from observed KiwiSDR stream headers.
const (
	flagADCOverrun  = 0x02
	flagStereo      = 0x08
	flagCompressed  = 0x10
	flagLittleEndLs = 0x80

	// tuneDrainWindow bounds how long Tune waits, after sending the SET
	// line, swallowing audio that was still in flight from the previous
	// frequency (review M4). Past that instant the frames that arrive
	// are guaranteed post-tune, so the capture starts clean.
	tuneDrainWindow = 250 * time.Millisecond
)

// maxFrameBytes caps one websocket frame. Kiwi audio and control frames are
// a few KB; 1 MiB leaves generous headroom while bounding a hostile or
// broken peer (the review asks for a 1-4 MiB read limit on every dial).
const maxFrameBytes = 1 << 20

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
	Waterfall    bool   // also open the /W/F waterfall connection
	WSPath       string // preflight-selected path style: "/ws/kiwi" or "legacy"
	WriteTimeout time.Duration
	// AllowPrivate permits dialing loopback/private hosts (local receivers,
	// tests). Leave false for directory-sourced stations.
	AllowPrivate bool
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
	secure  bool   // listing URL was https, so dial wss
	opts    Options
	ws      *websocket.Conn
	adpcm   adpcmState
	curFreq int64
	curMode string
	// discardAudioUntil suppresses in-flight pre-tune samples without an
	// extra timed websocket read (Gorilla makes read timeouts sticky).
	discardAudioUntil time.Time
	refused           error // set by handleText when the server refuses the session

	// stateMu fronts rate and info: handleText (the socket reader) writes
	// them while any listener can call SampleRate/InfoSnapshot (review
	// M7: unlocked rate/info).
	stateMu sync.RWMutex
	rate    float64
	info    Info
}

// New parses "host:port" or any http(s)/ws(s) URL the directory lists.
func New(server string, opts Options) (*Client, error) {
	base := server
	secure := false
	if strings.Contains(server, "://") {
		u, err := url.Parse(server)
		if err != nil {
			return nil, err
		}
		base = u.Host
		secure = u.Scheme == "https" || u.Scheme == "wss"
	}
	if base == "" {
		return nil, fmt.Errorf("kiwisdr: empty host")
	}
	if opts.IdleTimeout == 0 {
		opts.IdleTimeout = 30 * time.Second
	}
	return &Client{base: base, secure: secure, opts: opts, rate: 12000}, nil
}

// Connect opens the stream and starts audio flowing. It does not tune; call
// Tune separately so repeated reconnections replay the last tuning.
func (c *Client) Connect(ctx context.Context) error {
	timestamp := time.Now().Unix()
	path := ""
	if c.opts.WSPath == "/ws/kiwi" {
		path = "/ws/kiwi"
	}
	scheme := "ws"
	if c.secure {
		scheme = "wss"
	}
	uri := fmt.Sprintf("%s://%s%s/%d/SND", scheme, c.base, path, timestamp)
	dialer := websocket.Dialer{HandshakeTimeout: c.opts.OpenTimeout}
	if !c.opts.AllowPrivate {
		dialer.NetDialContext = common.PublicDialContext(c.opts.OpenTimeout)
	}
	ws, response, err := dialer.DialContext(ctx, uri, nil)
	if err != nil {
		if response != nil {
			snippet := ""
			if response.Body != nil {
				body, _ := io.ReadAll(io.LimitReader(response.Body, 512))
				snippet = strings.TrimSpace(string(body))
			}
			return fmt.Errorf("kiwisdr: %s: websocket HTTP %s server=%q body=%q: %w", c.base, response.Status, response.Header.Get("Server"), snippet, err)
		}
		return fmt.Errorf("kiwisdr: %s: %w", c.base, err)
	}
	c.ws = ws
	c.ws.SetReadLimit(maxFrameBytes)
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
	err := c.ws.WriteMessage(websocket.TextMessage, []byte(line))
	if err == nil {
		// Suppress audio that may still be in flight from the previous
		// frequency. Do not perform a timed websocket read here: Gorilla
		// treats a read timeout as permanent for that connection, which
		// would poison the next Record. Record continues reading normally
		// and drops binary SND/WEFAX frames during this bounded settle window;
		// text MSG frames are still processed for session state.
		c.discardAudioUntil = time.Now().Add(tuneDrainWindow)
	}
	return err
}

func (c *Client) reconnectForTune() error {
	if c.ws != nil {
		_ = c.ws.Close()
		c.ws = nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.opts.OpenTimeout)
	defer cancel()
	return c.Connect(ctx)
}

// Info snapshot of what the server has announced so far.
func (c *Client) InfoSnapshot() Info {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return c.info
}

// SampleRate is the negotiated decode rate, only valid once audio flows.
func (c *Client) SampleRate() int {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return int(math.Round(c.rate))
}

// Record collects duration of audio and returns samples plus rate. The
// returned context and duration deadlines are honoured throughout, including
// while waiting to reconnect; reconnects are capped so a flapping server
// cannot keep a recording slot alive forever.
func (c *Client) Record(ctx context.Context, duration time.Duration) ([]int16, int, error) {
	if c.ws == nil {
		return nil, 0, fmt.Errorf("kiwisdr: not connected")
	}
	deadline := time.NewTimer(duration)
	defer deadline.Stop()
	end := time.Now().Add(duration)
	keepalive := time.NewTicker(time.Second)
	defer keepalive.Stop()
	var out []int16
	rate := 12000
	reconnects := 0
	for {
		if c.refused != nil {
			err := c.refused
			c.refused = nil
			return out, rate, err
		}
		// ReadMessage blocks, so check the caller's deadlines before
		// committing to another read.
		select {
		case <-ctx.Done():
			return out, rate, ctx.Err()
		case <-deadline.C:
			return out, rate, nil
		case <-keepalive.C:
			c.ws.SetWriteDeadline(time.Now().Add(writeDeadline(c)))
			_ = c.ws.WriteMessage(websocket.TextMessage, []byte("SET keepalive"))
		default:
		}
		// Bound the read by the nearest caller deadline too, so a silent
		// server cannot park Record past ctx or the requested duration.
		readTimeout := c.opts.IdleTimeout
		if d, ok := ctx.Deadline(); ok {
			if until := time.Until(d); until < readTimeout {
				readTimeout = until
			}
		}
		if until := time.Until(end); until < readTimeout {
			readTimeout = until
		}
		if readTimeout < 50*time.Millisecond {
			readTimeout = 50 * time.Millisecond
		}
		c.ws.SetReadDeadline(time.Now().Add(readTimeout))
		kind, data, err := c.ws.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return out, rate, ctx.Err()
			}
			// The socket's deadline can expire at `end` just before the timer
			// goroutine delivers deadline.C. Treat that read timeout as the
			// requested duration completing, not as a receiver failure.
			if !time.Now().Before(end) {
				return out, rate, nil
			}
			select {
			case <-deadline.C:
				return out, rate, nil
			default:
			}
			if websocket.IsUnexpectedCloseError(err) && c.opts.Reconnect && reconnects < maxRecordReconnects {
				reconnects++
				wait := time.NewTimer(500 * time.Millisecond)
				select {
				case <-ctx.Done():
					wait.Stop()
					return out, rate, ctx.Err()
				case <-deadline.C:
					wait.Stop()
					return out, rate, nil
				case <-wait.C:
				}
				if err2 := c.Connect(ctx); err2 == nil {
					continue
				}
			}
			return out, rate, err
		}
		reconnects = 0
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
		if time.Now().Before(c.discardAudioUntil) {
			continue
		}
		body := data[3:]
		if len(body) < 7 {
			continue
		}
		flags := body[0]
		seq := binary.LittleEndian.Uint32(body[1:5])
		smeter10 := int(binary.BigEndian.Uint16(body[5:7])) // raw is 10*(dBm+127)
		smeter := smeter10 - 127*10
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

// maxRecordReconnects bounds consecutive mid-record reconnects. A server
// that drops every session right after connect must surface as an error,
// not keep the receiver slot busy retrying.
const maxRecordReconnects = 3

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
				c.stateMu.Lock()
				c.rate = f
				c.stateMu.Unlock()
			}
		case "bandwidth":
			if f, err := strconv.ParseFloat(value, 64); err == nil {
				c.stateMu.Lock()
				c.info.BandwidthKHz = f
				c.stateMu.Unlock()
			}
		case "version_maj", "version_min":
			// version messages land as separate pairs; keep both and join later.
		case "too_busy":
			// "too_busy=N": all N receiver slots are taken.
			c.refused = &BusyError{Host: c.base, Slots: atoiOr(value, 0)}
		case "badp":
			// badp=0 means the password was accepted; only 1 is a refusal.
			if value == "1" {
				c.refused = fmt.Errorf("%w: %s", ErrBadPassword, c.base)
			}
		case "down":
			c.refused = fmt.Errorf("%w: %s", ErrDown, c.base)
		case "rx_chans":
			if n, err := strconv.Atoi(value); err == nil {
				c.stateMu.Lock()
				c.info.RXChannels = n
				c.stateMu.Unlock()
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
			c.stateMu.Lock()
			c.info.Version = maj + "." + min
			c.stateMu.Unlock()
		}
	}
	if value, ok := extractURLEncoded(body, "load_cfg"); ok {
		c.stateMu.Lock()
		parseRxGPS(value, &c.info)
		c.stateMu.Unlock()
	}
}
