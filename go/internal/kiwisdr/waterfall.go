package kiwisdr

// waterfall.go: a clean-room client for the KiwiSDR /W/F waterfall stream.
//
// Wire format verified against a live public KiwiSDR v1.902 server
// (October 2026) and the public KiwiSDR server source (v1.711 era):
//
//	W/F packet: 16 byte header + bins
//	  [0:4]  "W/F\0" marker
//	  [4:8]  x_bin (uint32 LE): left edge of this line in zoom-level bins
//	  [8:12] flags|zoom (uint32 LE): zoom in the low 16 bits,
//	         bit 16 set when the payload is compressed
//	  [12:16] seq (uint32 LE)
//	  [16:]  one unsigned byte per bin
//
// Bin values are dB mapped by the server: dB is clamped to [-200, 0],
// decremented by one and cast to u1_t, so the inverse is int8(b)+1.
//
// Compression: the client negotiates wf_comp=0 (uncompressed), which the
// server supports officially. The compressed framing changed after the
// v1.711 public source release and compressed payloads are skipped here;
// uncompressed frames are exactly 1024 bins and cheap enough for
// monitoring. No external client implementation is incorporated.

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

const (
	wfFlagCompression = 0x00010000

	// defaultWFWidth is the bin count servers advertise (MSG wf_fft_size)
	// and send per line. Frames with a different payload length are still
	// accepted; this is only the pre-negotiation fallback.
	defaultWFWidth = 1024

	// defaultWFSpeed asks for the "slow" frame rate (server enum
	// 0=off,1=1fps,2=slow,3=med,4=fast). Monitoring needs a line every
	// few seconds; slow keeps the receiver's load and our traffic down.
	defaultWFSpeed = 2

	// wfStaleAfter mirrors the OpenWebRX freshness gate: a frame older
	// than this is a frozen picture, not live spectrum.
	wfStaleAfter = time.Minute
)

// WaterfallFrame is one decoded waterfall line.
type WaterfallFrame struct {
	XBin       uint32
	Zoom       uint16
	Seq        uint32
	Bins       []float64 // dB, see the mapping note above
	ReceivedAt time.Time
}

// WaterfallClient is one live /W/F connection. It is read-only: the only
// commands sent are the negotiation SET lines.
type WaterfallClient struct {
	base   string
	secure bool
	opts   Options

	ws     *websocket.Conn
	done   chan struct{}
	closed bool

	stateMu     sync.RWMutex
	fftSize     int
	centerHz    float64
	bandwidthHz float64
	latest      WaterfallFrame
	haveLatest  bool
	dead        bool
	refused     error
}

// NewWaterfall parses "host:port" or any http(s)/ws(s) URL, like New.
func NewWaterfall(server string, opts Options) (*WaterfallClient, error) {
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
		return nil, fmt.Errorf("kiwisdr waterfall: empty host")
	}
	return &WaterfallClient{base: base, secure: secure, opts: opts, done: make(chan struct{})}, nil
}

// Connect opens the stream, negotiates an uncompressed full-panorama
// waterfall and starts the read loop. Zoom 0 covers the receiver's whole
// band in one frame; narrower zooms are a follow-up.
func (c *WaterfallClient) Connect(ctx context.Context) error {
	timestamp := time.Now().Unix()
	path := ""
	if c.opts.WSPath == "/ws/kiwi" {
		path = "/ws/kiwi"
	}
	scheme := "ws"
	if c.secure {
		scheme = "wss"
	}
	uri := fmt.Sprintf("%s://%s%s/%d/W/F", scheme, c.base, path, timestamp)
	openTimeout := c.opts.OpenTimeout
	if openTimeout == 0 {
		openTimeout = 15 * time.Second
	}
	dialer := websocket.Dialer{HandshakeTimeout: openTimeout}
	if !c.opts.AllowPrivate {
		dialer.NetDialContext = common.PublicDialContext(openTimeout)
	}
	ws, response, err := dialer.DialContext(ctx, uri, nil)
	if err != nil {
		if response != nil {
			snippet := ""
			if response.Body != nil {
				body, _ := io.ReadAll(io.LimitReader(response.Body, 512))
				snippet = strings.TrimSpace(string(body))
			}
			return fmt.Errorf("kiwisdr waterfall: %s: websocket HTTP %s server=%q body=%q: %w", c.base, response.Status, response.Header.Get("Server"), snippet, err)
		}
		return fmt.Errorf("kiwisdr waterfall: %s: %w", c.base, err)
	}
	c.ws = ws
	c.ws.SetReadLimit(maxFrameBytes)
	writeTimeout := c.opts.WriteTimeout
	if writeTimeout == 0 {
		writeTimeout = 10 * time.Second
	}
	write := func(line string) error {
		_ = ws.SetWriteDeadline(time.Now().Add(writeTimeout))
		return ws.WriteMessage(websocket.TextMessage, []byte(line))
	}
	ident := c.opts.IdentUser
	if ident == "" {
		ident = "sdrstream"
	}
	for _, line := range []string{
		"SET auth t=kiwi p=" + c.opts.Password,
		"SET ident_user=" + ident,
		"SET zoom=0 start=0",
		"SET maxdb=-10 mindb=-110",
		fmt.Sprintf("SET wf_speed=%d", defaultWFSpeed),
		"SET wf_comp=0",
	} {
		if err := write(line); err != nil {
			ws.Close()
			return err
		}
	}
	go c.readLoop()
	return nil
}

// Close shuts the socket down.
func (c *WaterfallClient) Close() error {
	c.stateMu.Lock()
	if c.closed {
		c.stateMu.Unlock()
		return nil
	}
	c.closed = true
	c.stateMu.Unlock()
	if c.ws != nil {
		err := c.ws.Close()
		c.ws = nil
		return err
	}
	return nil
}

// Spectrum returns the latest waterfall line mapped to absolute
// frequencies, in the shape monitor.Source expects. ok is false when no
// frame has arrived, the connection died, or the newest frame is stale.
func (c *WaterfallClient) Spectrum() (bins []float64, centreHz, spanHz float64, ok bool) {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	if !c.haveLatest || c.bandwidthHz <= 0 {
		return nil, 0, 0, false
	}
	if c.dead && time.Since(c.latest.ReceivedAt) > 10*time.Second {
		return nil, 0, 0, false
	}
	if time.Since(c.latest.ReceivedAt) > wfStaleAfter {
		return nil, 0, 0, false
	}
	fft := c.fftSize
	if fft <= 0 {
		fft = defaultWFWidth
	}
	binsWide := float64(fft) * math.Pow(2, float64(c.latest.Zoom))
	binHz := c.bandwidthHz / binsWide
	startHz := c.centerHz - c.bandwidthHz/2 + float64(c.latest.XBin)*binHz
	spanHz = float64(len(c.latest.Bins)) * binHz
	centreHz = startHz + spanHz/2
	out := make([]float64, len(c.latest.Bins))
	copy(out, c.latest.Bins)
	return out, centreHz, spanHz, true
}

// parseFrame decodes one W/F packet. Compressed payloads are reported
// with ok=false: we negotiate uncompressed, so a compressed frame means
// the server ignored wf_comp=0 and the bins would be garbage.
func parseFrame(data []byte, receivedAt time.Time) (WaterfallFrame, bool) {
	if len(data) < 17 || string(data[:3]) != "W/F" {
		return WaterfallFrame{}, false
	}
	flagsZoom := binary.LittleEndian.Uint32(data[8:12])
	if flagsZoom&wfFlagCompression != 0 {
		return WaterfallFrame{}, false
	}
	payload := data[16:]
	bins := make([]float64, len(payload))
	for i, b := range payload {
		bins[i] = float64(int8(b)) + 1
	}
	return WaterfallFrame{
		XBin:       binary.LittleEndian.Uint32(data[4:8]),
		Zoom:       uint16(flagsZoom & 0xffff),
		Seq:        binary.LittleEndian.Uint32(data[12:16]),
		Bins:       bins,
		ReceivedAt: receivedAt,
	}, true
}

// readLoop consumes frames until the socket fails or Close is called.
func (c *WaterfallClient) readLoop() {
	defer func() {
		c.stateMu.Lock()
		c.dead = true
		c.stateMu.Unlock()
	}()
	idle := c.opts.IdleTimeout
	if idle == 0 {
		idle = 30 * time.Second
	}
	for {
		c.ws.SetReadDeadline(time.Now().Add(idle))
		kind, data, err := c.ws.ReadMessage()
		if err != nil {
			return
		}
		if kind == websocket.TextMessage {
			c.handleText(string(data))
			continue
		}
		if len(data) >= 4 && string(data[:3]) == "MSG" {
			c.handleText("MSG " + string(data[4:]))
			continue
		}
		frame, ok := parseFrame(data, time.Now())
		if !ok {
			continue
		}
		c.stateMu.Lock()
		c.latest = frame
		c.haveLatest = true
		c.stateMu.Unlock()
	}
}

// handleText picks the waterfall geometry out of MSG lines.
func (c *WaterfallClient) handleText(text string) {
	if !strings.HasPrefix(text, "MSG ") {
		return
	}
	for _, pair := range strings.Fields(text[4:]) {
		name, value, found := strings.Cut(pair, "=")
		if !found {
			continue
		}
		switch name {
		case "wf_fft_size":
			if n, err := strconv.Atoi(value); err == nil && n > 0 {
				c.stateMu.Lock()
				c.fftSize = n
				c.stateMu.Unlock()
			}
		case "center_freq":
			if f, err := strconv.ParseFloat(value, 64); err == nil {
				c.stateMu.Lock()
				c.centerHz = f
				c.stateMu.Unlock()
			}
		case "bandwidth":
			if f, err := strconv.ParseFloat(value, 64); err == nil {
				c.stateMu.Lock()
				c.bandwidthHz = f
				c.stateMu.Unlock()
			}
		case "badp":
			if value == "1" {
				c.stateMu.Lock()
				c.refused = fmt.Errorf("%w: %s", ErrBadPassword, c.base)
				c.stateMu.Unlock()
			}
		}
	}
}
