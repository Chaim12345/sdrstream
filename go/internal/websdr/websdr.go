// Package websdr is a client for WebSDR servers (the websdr.org family).
//
// WebSDR serves a raw WebSocket endpoint at /~~stream. There is no handshake:
// the server starts pushing binary frames immediately. Retuning is requested
// by sending a plain HTTP-style text frame:
//
//	GET /~~param?f=<kHz>&band=0&lo=<kHz>&hi=<kHz>&mode=<code>&name=
//
// Mode codes are USB=0, LSB=0, CW=0, AM=1, AMSYNC=2 and FM=4; SSB and CW
// differ only in the passband offsets lo/hi.
//
// Binary frames carry the mixed table/LPC stream decoded by the vocoder
// package. Control bytes observed in the stream are 0x81 (sample rate),
// 0x82 (AGC gain), 0x83 (audio flags), 0x85 (true frequency), 0x86 (filter
// switch) and 0x87 (timestamp).
package websdr

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/common"
	"github.com/chaim12345/sdrstream/go/internal/vocoder"
	"github.com/gorilla/websocket"
)

// StreamPath is the WebSDR WebSocket endpoint.
const StreamPath = "/~~stream"

// maxFrameBytes caps one websocket frame (review: 1-4 MiB read limit on
// every dial). WebSDR frames are small vocoder blocks; 1 MiB is generous.
const maxFrameBytes = 1 << 20

// writeTimeout bounds every control write (tune, squelch, retune after
// reconnect) so a stuck peer cannot wedge the writer.
const writeTimeout = 10 * time.Second

// Chunk is one decoded block of WebSDR audio.
type Chunk struct {
	Samples    []int16
	Samplerate int
	Sequence   int
}

// Status is state reported by the server inside the audio stream.
type Status struct {
	Samplerate    int
	SamplerateSet bool
	AGC           int
	AudioFlags    int
	TrueFrequency float64
	Timestamp     int
	FilterSwitch  int
	Smeter        int
	HasValues     bool
}

// Event is one message pushed to the caller: audio, text, or an error.
type Event struct {
	Type  string      // "audio", "text", "error", "reconnecting"
	Value interface{} // Chunk, string, error
}

// Options configures a Client.
type Options struct {
	Reconnect        bool
	ReconnectInitial time.Duration
	ReconnectMax     time.Duration
	OpenTimeout      time.Duration
	IdleTimeout      time.Duration
	// AllowPrivate permits dialing loopback/private hosts (local receivers,
	// tests). Leave false for directory-sourced stations.
	AllowPrivate bool
}

// DefaultOptions are polite defaults for a public server.
func DefaultOptions() Options {
	return Options{
		Reconnect: true, ReconnectInitial: time.Second,
		ReconnectMax: 30 * time.Second, OpenTimeout: 15 * time.Second,
		IdleTimeout: 30 * time.Second,
	}
}

// Client streams audio from a WebSDR server.
type Client struct {
	URL     string
	Origin  string
	Options Options

	Status     Status
	Samplerate int
	LastError  error

	mu        sync.Mutex
	conn      *websocket.Conn
	events    chan Event
	quit      chan struct{}
	vocoder   *vocoder.Vocoder
	sequence  int
	tuning    string
	closeOnce sync.Once
}

// New builds a client for a WebSDR server URL. The page URL may carry a
// subpath selecting a frontend (e.g. eshail.batc.org.uk/nb/), but the
// WebSDR protocol always serves its stream from the server root:
// wss://host/~~stream, as websdr-sound.js connects.
func New(rawURL string, options Options) (*Client, error) {
	base, err := common.NormalizeHTTPURL(rawURL)
	if err != nil {
		return nil, err
	}
	parsed, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	root := parsed.Scheme + "://" + parsed.Host
	streamURL, err := common.WebSocketURL(root, StreamPath)
	if err != nil {
		return nil, err
	}
	origin, err := originFor(rawURL)
	if err != nil {
		return nil, err
	}
	if options.OpenTimeout == 0 {
		options.OpenTimeout = 15 * time.Second
	}
	return &Client{
		URL:     streamURL,
		Origin:  origin,
		Options: options,
		events:  make(chan Event, 256),
		quit:    make(chan struct{}),
		vocoder: vocoder.New(),
	}, nil
}

func originFor(rawURL string) (string, error) {
	base, err := common.NormalizeHTTPURL(rawURL)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	scheme := "http"
	if parsed.Scheme == "https" || parsed.Scheme == "wss" {
		scheme = "https"
	}
	return scheme + "://" + parsed.Host, nil
}

// Events returns the channel every message is published to.
func (c *Client) Events() <-chan Event { return c.events }

// Connect opens the stream and starts the background reader.
func (c *Client) Connect(ctx context.Context) error {
	return c.connect(ctx)
}

// dialer builds the websocket dialer, guarded against non-public targets
// unless the caller opted in to private/loopback receivers.
func (c *Client) dialer() *websocket.Dialer {
	d := &websocket.Dialer{HandshakeTimeout: c.Options.OpenTimeout}
	if !c.Options.AllowPrivate {
		d.NetDialContext = common.PublicDialContext(c.Options.OpenTimeout)
	}
	return d
}

func (c *Client) connect(ctx context.Context) error {
	c.vocoder.Reset()
	c.sequence = 0
	// WebSDR validates the Origin header and drops the connection without it.
	header := http.Header{}
	header.Set("Origin", c.Origin)
	conn, _, err := c.dialer().DialContext(ctx, c.URL, header)
	if err != nil {
		return err
	}
	conn.SetReadLimit(maxFrameBytes)
	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()
	go c.readLoop(ctx, conn)
	return nil
}

// Close stops the reader and closes the socket. Closing the connection is
// what actually unblocks a read parked in ReadMessage; closing quit alone
// left the socket open until the server noticed.
func (c *Client) Close() {
	c.closeOnce.Do(func() {
		close(c.quit)
		c.mu.Lock()
		if c.conn != nil {
			c.conn.Close()
		}
		c.mu.Unlock()
	})
}

func (c *Client) readLoop(ctx context.Context, conn *websocket.Conn) {
	defer conn.Close()
	delay := c.Options.ReconnectInitial
	if delay == 0 {
		delay = time.Second
	}
	for {
		select {
		case <-c.quit:
			return
		case <-ctx.Done():
			return
		default:
		}
		if c.Options.IdleTimeout > 0 {
			_ = conn.SetReadDeadline(time.Now().Add(c.Options.IdleTimeout))
		}
		messageType, data, err := conn.ReadMessage()
		if err == nil {
			// The connection works, so the next drop starts from the
			// initial backoff again. Without this reset the delay only
			// ever grew: one bad afternoon left every later reconnect a
			// ReconnectMax away for the life of the client.
			delay = c.Options.ReconnectInitial
			if delay == 0 {
				delay = time.Second
			}
		} else {
			select {
			case <-c.quit:
				return
			case <-ctx.Done():
				return
			default:
			}
			c.LastError = err
			c.publish(Event{Type: "error", Value: err})
			if !c.Options.Reconnect {
				return
			}
			c.publish(Event{Type: "reconnecting", Value: delay})
			jitter := time.Duration(rand.Float64() * float64(delay) * 0.1)
			select {
			case <-time.After(delay + jitter):
			case <-c.quit:
				return
			case <-ctx.Done():
				return
			}
			delay *= 2
			if delay > c.Options.ReconnectMax && c.Options.ReconnectMax > 0 {
				delay = c.Options.ReconnectMax
			}
			header := http.Header{}
			header.Set("Origin", c.Origin)
			dialCtx, dialCancel := context.WithTimeout(ctx, c.Options.OpenTimeout)
			newConn, _, dialErr := c.dialer().DialContext(dialCtx, c.URL, header)
			dialCancel()
			if dialErr != nil {
				c.LastError = dialErr
				continue
			}
			newConn.SetReadLimit(maxFrameBytes)
			conn.Close()
			conn = newConn
			c.mu.Lock()
			c.conn = newConn
			c.mu.Unlock()
			c.vocoder.Reset()
			c.sequence = 0
			if c.tuning != "" {
				// A retune is a data frame, not a control frame: send it
				// with WriteMessage under a write deadline.
				_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
				_ = conn.WriteMessage(websocket.TextMessage,
					[]byte("GET /~~param?"+c.tuning))
			}
			continue
		}
		if messageType == websocket.BinaryMessage {
			c.handleBinary(data)
		} else {
			c.publish(Event{Type: "text", Value: string(data)})
		}
	}
}

func (c *Client) handleBinary(data []byte) {
	samples := c.vocoder.Decode(data)
	c.mu.Lock()
	samplerate := c.vocoder.Samplerate
	c.Samplerate = samplerate
	c.Status.Samplerate = samplerate
	c.Status.SamplerateSet = true
	c.Status.AGC = c.vocoder.AGC()
	c.Status.AudioFlags = c.vocoder.AudioFlags()
	c.Status.HasValues = true
	if len(samples) > 0 {
		chunk := Chunk{Samples: samples, Samplerate: samplerate, Sequence: c.sequence}
		c.sequence++
		c.mu.Unlock()
		c.publish(Event{Type: "audio", Value: chunk})
		return
	}
	c.mu.Unlock()
}

func (c *Client) publish(event Event) {
	select {
	case c.events <- event:
	default:
		// A slow consumer must not stall the reader; drop the oldest.
		select {
		case <-c.events:
		default:
		}
		select {
		case c.events <- event:
		default:
		}
	}
}

// Tune retunes the receiver. frequencyKHz is the carrier in kHz.
func (c *Client) Tune(ctx context.Context, frequencyKHz float64, mode string, bandwidthHz *float64) error {
	params, err := common.WebSDRTuningParams(frequencyKHz, mode, bandwidthHz, nil, nil, 0, "")
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.tuning = params
	conn := c.conn
	if conn == nil {
		c.mu.Unlock()
		return nil
	}
	// Serialize this bounded write with the reconnect loop's connection
	// swap. Otherwise Tune can snapshot the dying socket just before the
	// reader closes it and return the incidental "websocket: close sent"
	// error; the reconnect loop will replay the latest tuning on its socket.
	_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	err = conn.WriteMessage(websocket.TextMessage, []byte("GET /~~param?"+params))
	c.mu.Unlock()
	c.drainTuneBuffer()
	// The read loop's close handler may already have sent a close control
	// frame on this socket. Its reconnect path replays c.tuning on the new
	// socket, so this expected close-sent write is not a failed retune.
	if errors.Is(err, websocket.ErrCloseSent) {
		return nil
	}
	return err
}

// drainTuneBuffer drops buffered events from before the tune. The buffer
// holds the previous frequency's audio (and stale meter text); without
// this the next Record starts with the old broadcast, per review M4.
func (c *Client) drainTuneBuffer() {
	for {
		select {
		case <-c.events:
			continue
		default:
			return
		}
	}
}

// sendParam sends a raw /~~param query string (e.g. "squelch=3").
func (c *Client) sendParam(ctx context.Context, params string) {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return
	}
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_ = conn.WriteMessage(websocket.TextMessage, []byte("GET /~~param?"+params))
}

// SetSquelch sets the squelch level; 0 disables it.
func (c *Client) SetSquelch(ctx context.Context, value float64) {
	c.sendParam(ctx, fmt.Sprintf("squelch=%s", formatFloat(value)))
}

// SetNoiseReduction sets the noise-reduction level; -999 turns it off.
func (c *Client) SetNoiseReduction(ctx context.Context, level float64) {
	c.sendParam(ctx, fmt.Sprintf("noisered=%s", formatFloat(level)))
}

// SetAutoNotch enables or disables the automatic notch filter.
func (c *Client) SetAutoNotch(ctx context.Context, value float64) {
	c.sendParam(ctx, fmt.Sprintf("autonotch=%s", formatFloat(value)))
}

func formatFloat(value float64) string {
	return strings.TrimSpace(fmt.Sprintf("%f", value))
}

// Record collects audio for duration and returns the concatenated samples
// and the samplerate they were captured at. A dead reader must not look
// like a quiet receiver: when the stream fails and no audio arrived, the
// reader's error is returned instead of clean silence.
func (c *Client) Record(ctx context.Context, duration time.Duration) ([]int16, int, error) {
	var samples []int16
	rate := 8000
	var lastErr error
	deadline := time.NewTimer(duration)
	defer deadline.Stop()
	for {
		select {
		case <-deadline.C:
			if len(samples) == 0 && lastErr != nil {
				return samples, rate, lastErr
			}
			return samples, rate, nil
		case <-ctx.Done():
			return samples, rate, ctx.Err()
		case event, ok := <-c.events:
			if !ok {
				if lastErr != nil {
					return samples, rate, lastErr
				}
				return samples, rate, nil
			}
			switch event.Type {
			case "audio":
				chunk := event.Value.(Chunk)
				samples = append(samples, chunk.Samples...)
				rate = chunk.Samplerate
			case "error":
				if err, ok := event.Value.(error); ok {
					lastErr = err
				}
			}
		}
	}
}

// DrainAudio discards buffered audio events. Call it after Tune: frames
// captured at the old frequency would otherwise pollute the start of the
// next recording.
func (c *Client) DrainAudio() {
	// Pull everything out first; re-publishing inside the loop could
	// cycle a full buffer of control events forever.
	var keep []Event
	for {
		select {
		case event := <-c.events:
			if event.Type != "audio" {
				keep = append(keep, event)
			}
		default:
			for _, event := range keep {
				c.publish(event)
			}
			return
		}
	}
}

// Concat flattens chunks into a single sample slice.
func Concat(chunks []Chunk) []int16 {
	total := 0
	for _, chunk := range chunks {
		total += len(chunk.Samples)
	}
	out := make([]int16, 0, total)
	for _, chunk := range chunks {
		out = append(out, chunk.Samples...)
	}
	return out
}
