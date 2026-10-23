// Package openwebrx is a client for OpenWebRX and OpenWebRX+ servers.
//
// Connect to ws(s)://<host>/ws/ and send a text handshake:
//
//	SERVER DE CLIENT client=<name> type=receiver
//
// The server replies with "CLIENT DE SERVER ..." followed by JSON text frames
// (config, profiles, modes, smeter, ...). After the client sends
// {"type": "dspcontrol", "action": "start"} the server begins streaming binary
// frames whose first byte is an opcode:
//
//	0x01  FFT         waterfall spectrum, IMA ADPCM, no sync markers
//	0x02  AUDIO       12 kHz demodulated audio, IMA ADPCM with SYNC markers
//	0x03  SECONDARY FFT
//	0x04  HD AUDIO    high-definition audio (e.g. 44.1 kHz)
//
// Audio payloads may be adpcm compressed or raw 16-bit PCM, depending on the
// audio_compression value advertised in the config message.
package openwebrx

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/adpcm"
	"github.com/chaim12345/sdrstream/go/internal/common"
	"github.com/gorilla/websocket"
	"net/http"
)

// Opcode meanings for binary frames.
const (
	OpcodeFFT          = 0x01
	OpcodeAudio        = 0x02
	OpcodeSecondaryFFT = 0x03
	OpcodeHDAudio      = 0x04
)

// fftPadN is the number of codec padding samples that precede the spectrum
// in every FFT frame (COMPRESS_FFT_PAD_N in upstream openwebrx.js).
const fftPadN = 10

// maxFrameBytes caps one websocket frame (review: 1-4 MiB read limit on
// every dial). FFT frames carry a float32 per bin, so 4 MiB.
const maxFrameBytes = 4 << 20

// writeTimeout bounds every control write (handshake, tune, profile
// select, DSP start) so a stuck peer cannot wedge the writer.
const writeTimeout = 10 * time.Second

// DefaultOutputRate is the audio rate most servers deliver.
const DefaultOutputRate = 12000

// DefaultHDOutputRate is the HD audio rate.
const DefaultHDOutputRate = 44100

// AudioChunk is one decoded block of audio.
type AudioChunk struct {
	Samples    []int16
	Samplerate int
	Sequence   int
	Compressed bool
	Kind       string // "audio" or "hd_audio"
}

// Config is the server configuration advertised in the config message.
type Config struct {
	CenterFreq         *int64   `json:"center_freq"`
	SampRate           *float64 `json:"samp_rate"`
	StartMod           *string  `json:"start_mod"`
	StartFreq          *int64   `json:"start_freq"`
	AudioCompression   *string  `json:"audio_compression"`
	FFTCompression     *string  `json:"fft_compression"`
	AudioRates         []int    `json:"audio_rates"`
	HDAudioRates       []int    `json:"hd_audio_rates"`
	HDAudioCompression *string  `json:"hd_audio_compression"`
	MaxClients         *int     `json:"max_clients"`
}

// Event is one message pushed to the caller.
type Event struct {
	Type      string // "audio", "fft", "config", "profiles", "modes", "smeter", "text", "error", "reconnecting"
	Value     interface{}
	Secondary bool // fft only
}

// Client streams demodulated audio from an OpenWebRX server.
type Client struct {
	URL        string
	ClientName string

	Config    Config
	Profiles  []map[string]interface{}
	Modes     []map[string]interface{}
	Smeter    *float64
	Clients   int
	LastError error

	events      chan Event
	audioEvents chan Event
	eventsOnce  sync.Once
	quit        chan struct{}
	conn        *websocket.Conn
	mu          sync.Mutex
	audio       *adpcm.Decoder
	hd          *adpcm.Decoder
	fft         *adpcm.Decoder
	sequence    int
	tuning      map[string]interface{}
	profile     string
	closeOnce   sync.Once
	outputRate  int
	hdRate      int
	options     Options
}

// Options configures a Client.
type Options struct {
	OutputRate   int
	HDOutputRate int
	Reconnect    bool
	OpenTimeout  time.Duration
	IdleTimeout  time.Duration
	// ReconnectInitial is the first backoff delay after a dropped
	// connection; it doubles per failed attempt up to ReconnectMax and
	// resets as soon as a message arrives. Zero uses sane defaults.
	ReconnectInitial time.Duration
	ReconnectMax     time.Duration
	// AllowPrivate permits dialing loopback/private hosts (local receivers,
	// tests). Leave false for directory-sourced stations.
	AllowPrivate bool
}

// DefaultOptions returns polite defaults.
func DefaultOptions() Options {
	return Options{OutputRate: DefaultOutputRate, HDOutputRate: DefaultHDOutputRate,
		Reconnect: true, OpenTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second,
		ReconnectInitial: time.Second, ReconnectMax: 30 * time.Second}
}

// reconnectAttempts bounds the retries after one connection drops: a
// receiver that stays dead must still reach its terminal state (error
// events, closed events channel) instead of retrying forever, which is
// the same "looks alive but is dead" failure the terminal contract
// exists to prevent.
const reconnectAttempts = 5

// New builds a client for an OpenWebRX server URL.
func New(rawURL string, options Options) (*Client, error) {
	streamURL := rawURL
	if !strings.Contains(rawURL, "://") || (strings.Split(rawURL, "://")[0] != "ws" && strings.Split(rawURL, "://")[0] != "wss") {
		var err error
		streamURL, err = common.WebSocketURL(rawURL, "/ws/")
		if err != nil {
			return nil, err
		}
	}
	if options.OutputRate == 0 {
		options.OutputRate = DefaultOutputRate
	}
	if options.HDOutputRate == 0 {
		options.HDOutputRate = DefaultHDOutputRate
	}
	if options.ReconnectInitial == 0 {
		options.ReconnectInitial = time.Second
	}
	if options.ReconnectMax == 0 {
		options.ReconnectMax = 30 * time.Second
	}
	return &Client{
		URL: streamURL, ClientName: "sdrstream",
		events:      make(chan Event, 256),
		audioEvents: make(chan Event, 256),
		quit:        make(chan struct{}),
		audio:       adpcm.New(), hd: adpcm.New(), fft: adpcm.New(),
		tuning:     map[string]interface{}{},
		outputRate: options.OutputRate,
		hdRate:     options.HDOutputRate,
		options:    options,
	}, nil
}

// Events returns the channel every message is published to.
func (c *Client) Events() <-chan Event { return c.events }

// Connect opens the WebSocket, performs the handshake, starts the DSP and
// returns the server's configuration.
func (c *Client) Connect(ctx context.Context) (Config, error) {
	dialer := &websocket.Dialer{HandshakeTimeout: 15 * time.Second}
	if !c.options.AllowPrivate {
		dialer.NetDialContext = common.PublicDialContext(15 * time.Second)
	}
	conn, _, err := dialer.DialContext(ctx, c.URL, http.Header{})
	if err != nil {
		return Config{}, err
	}
	conn.SetReadLimit(maxFrameBytes)
	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()

	c.audio.Reset()
	c.hd.Reset()
	c.fft.Reset()
	c.sequence = 0
	if err := c.handshake(ctx, conn); err != nil {
		conn.Close()
		return Config{}, err
	}
	go c.readLoop(ctx, conn)
	c.mu.Lock()
	config := c.Config
	c.mu.Unlock()
	return config, nil
}

// writeText sends one text frame with a bounded write deadline. Without it
// a dead/blocked peer wedges the scanner worker forever on a TCP buffer
// that will never drain (review H3); kiwisdr already does this.
const openwebRXWriteTimeout = 10 * time.Second

func writeText(conn *websocket.Conn, payload []byte) error {
	_ = conn.SetWriteDeadline(time.Now().Add(openwebRXWriteTimeout))
	return conn.WriteMessage(websocket.TextMessage, payload)
}

// handshake sends the client greeting, waits for the configuration, then
// sends the pending tuning request and starts the DSP. The handshake completes
// before the reader starts so only one goroutine ever reads from the socket.
func (c *Client) handshake(ctx context.Context, conn *websocket.Conn) error {
	greeting := fmt.Sprintf("SERVER DE CLIENT client=%s type=receiver", c.ClientName)
	if err := writeText(conn, []byte(greeting)); err != nil {
		return err
	}
	// The configuration arrives as several config messages and only the last
	// one carries the centre frequency. The profiles list follows it, so keep
	// reading a short grace period after the config so callers can enumerate
	// profiles right after Connect.
	configured := false
	var graceDeadline time.Time
	for {
		_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
		messageType, data, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		if messageType == websocket.BinaryMessage {
			continue
		}
		text := string(data)
		if strings.HasPrefix(text, "CLIENT DE SERVER") {
			params := map[string]interface{}{
				"type": "connectionproperties",
				"params": map[string]interface{}{
					"output_rate": c.outputRate, "hd_output_rate": c.hdRate}}
			payload, _ := json.Marshal(params)
			if err := writeText(conn, payload); err != nil {
				return err
			}
			continue
		}
		if c.handleText(text) && !configured {
			configured = true
			graceDeadline = time.Now().Add(2 * time.Second)
		}
		if configured {
			c.mu.Lock()
			profiles := len(c.Profiles)
			c.mu.Unlock()
			if profiles > 0 || time.Now().After(graceDeadline) {
				return c.startDSP(ctx, conn)
			}
		}
	}
}

// startDSP re-selects the profile the caller chose (a reconnect lands on
// the server's default profile), sends the pending tuning request, then
// the DSP start request.
func (c *Client) startDSP(ctx context.Context, conn *websocket.Conn) error {
	c.mu.Lock()
	profile := c.profile
	tuning := make(map[string]interface{}, len(c.tuning))
	for key, value := range c.tuning {
		tuning[key] = value
	}
	c.mu.Unlock()
	if profile != "" {
		message := map[string]interface{}{
			"type":   "selectprofile",
			"params": map[string]interface{}{"profile": profile},
		}
		payload, _ := json.Marshal(message)
		if err := writeText(conn, payload); err != nil {
			return err
		}
	}
	if len(tuning) > 0 {
		message := map[string]interface{}{"type": "dspcontrol"}
		for key, value := range tuning {
			message[key] = value
		}
		payload, _ := json.Marshal(message)
		if err := writeText(conn, payload); err != nil {
			return err
		}
	}
	start := map[string]interface{}{"type": "dspcontrol", "action": "start"}
	payload, _ := json.Marshal(start)
	return writeText(conn, payload)
}

func (c *Client) handleText(message string) bool {
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(message), &payload); err != nil {
		return false
	}
	kind, _ := payload["type"].(string)
	value := payload["value"]
	// The struct fields are read from other goroutines (Connect's return,
	// ProfileList, the handshake), so mutate them under mu; publish after
	// unlocking so a slow consumer cannot hold the lock.
	switch kind {
	case "config":
		c.mu.Lock()
		if fields, ok := value.(map[string]interface{}); ok {
			raw, _ := json.Marshal(fields)
			// Servers send several incremental config messages, so new fields
			// merge into the previous ones instead of replacing them; else
			// audio_compression is lost and ADPCM audio is decoded as raw PCM.
			_ = json.Unmarshal(raw, &c.Config)
		}
		config := c.Config
		c.mu.Unlock()
		c.publish(Event{Type: "config", Value: config})
		return config.CenterFreq != nil
	case "profiles":
		profiles := toMapSlice(value)
		c.mu.Lock()
		c.Profiles = profiles
		c.mu.Unlock()
		c.publish(Event{Type: "profiles", Value: profiles})
	case "modes":
		modes := toMapSlice(value)
		c.mu.Lock()
		c.Modes = modes
		c.mu.Unlock()
		c.publish(Event{Type: "modes", Value: modes})
	case "smeter":
		if number, ok := value.(float64); ok {
			c.mu.Lock()
			c.Smeter = &number
			c.mu.Unlock()
		}
		c.publish(Event{Type: "smeter", Value: value})
	case "receiver_details":
		c.publish(Event{Type: "receiver_details", Value: value})
	case "clients":
		if number, ok := value.(float64); ok {
			c.mu.Lock()
			c.Clients = int(number)
			c.mu.Unlock()
		}
		c.publish(Event{Type: "clients", Value: value})
	default:
		c.publish(Event{Type: kind, Value: value})
	}
	return false
}

func toMapSlice(value interface{}) []map[string]interface{} {
	rawList, ok := value.([]interface{})
	if !ok {
		return nil
	}
	out := make([]map[string]interface{}, 0, len(rawList))
	for _, item := range rawList {
		if m, ok := item.(map[string]interface{}); ok {
			out = append(out, m)
		}
	}
	return out
}

func (c *Client) handleBinary(data []byte) {
	if len(data) == 0 {
		return
	}
	opcode := data[0]
	payload := data[1:]
	switch opcode {
	case OpcodeAudio:
		compressed := c.Config.AudioCompression != nil && *c.Config.AudioCompression == "adpcm"
		samples := c.decode(payload, compressed, c.audio, true)
		if len(samples) > 0 {
			c.publishAudio(samples, c.outputRate, compressed, "audio")
		}
	case OpcodeHDAudio:
		compressed := c.Config.HDAudioCompression != nil && *c.Config.HDAudioCompression == "adpcm"
		samples := c.decode(payload, compressed, c.hd, true)
		if len(samples) > 0 {
			c.publishAudio(samples, c.hdRate, compressed, "hd_audio")
		}
	case OpcodeFFT, OpcodeSecondaryFFT:
		// The FFT payload is always dB values. When compression is off it
		// arrives as raw float32 (one value per bin); when it is on the
		// server sends ADPCM int16 that must be scaled by 100. Upstream
		// openwebrx.js: Float32Array directly, or codec output / 100.
		compressed := c.Config.FFTCompression != nil && *c.Config.FFTCompression == "adpcm"
		var db []float64
		if compressed {
			// Each FFT frame carries a fresh ADPCM state, so reset first.
			c.fft.Reset()
			s := c.fft.Decode(payload)
			if len(s) > fftPadN {
				s = s[fftPadN:]
			}
			db = make([]float64, len(s))
			for i, v := range s {
				db[i] = float64(v) / 100
			}
		} else {
			db = decodeFloat32(payload)
		}
		if len(db) > 0 {
			c.publish(Event{Type: "fft", Value: db, Secondary: opcode == OpcodeSecondaryFFT})
		}
	}
}

// decodeFloat32 reads a little-endian float32 array, the raw form of an
// uncompressed FFT frame.
func decodeFloat32(payload []byte) []float64 {
	n := len(payload) / 4
	out := make([]float64, n)
	for i := 0; i < n; i++ {
		out[i] = float64(math.Float32frombits(binary.LittleEndian.Uint32(payload[i*4:])))
	}
	return out
}

// SpectrumPeak is one peak in a server-side FFT frame.
type SpectrumPeak struct {
	Bin     int
	FreqHz  float64
	LevelDB float64
}

// AverageSpectra element-wise averages FFT frames to reduce noise. Frames
// of differing length are ignored beyond the shortest.
func AverageSpectra(frames [][]float64) []float64 {
	if len(frames) == 0 {
		return nil
	}
	n := len(frames[0])
	for _, f := range frames {
		if len(f) < n {
			n = len(f)
		}
	}
	out := make([]float64, n)
	for _, f := range frames {
		for i := 0; i < n; i++ {
			out[i] += f[i]
		}
	}
	for i := range out {
		out[i] /= float64(len(frames))
	}
	return out
}

// FindSpectrumPeaks locates narrow peaks in a dB spectrum. Because the
// server computes and calibrates the FFT, a peak here is real energy above
// the local noise, not an AGC artefact. Bin i maps to
// centreHz - spanHz/2 + i*spanHz/len(bins).
func FindSpectrumPeaks(bins []float64, centreHz, spanHz, thresholdDB float64) []SpectrumPeak {
	if len(bins) < 8 {
		return nil
	}
	sorted := append([]float64(nil), bins...)
	sort.Float64s(sorted)
	floor := sorted[len(sorted)/2] // median, noise-dominated
	limit := floor + thresholdDB
	binHz := spanHz / float64(len(bins))
	var peaks []SpectrumPeak
	for i := 1; i < len(bins)-1; i++ {
		if bins[i] <= limit {
			continue
		}
		if bins[i] >= bins[i-1] && bins[i] > bins[i+1] {
			peaks = append(peaks, SpectrumPeak{
				Bin:     i,
				FreqHz:  centreHz - spanHz/2 + (float64(i)+0.5)*binHz,
				LevelDB: bins[i],
			})
		}
	}
	return peaks
}

func (c *Client) decode(payload []byte, compressed bool, decoder *adpcm.Decoder, sync bool) []int16 {
	if compressed {
		if sync {
			return decoder.DecodeWithSync(payload)
		}
		return decoder.Decode(payload)
	}
	return decodePCM(payload)
}

func decodePCM(payload []byte) []int16 {
	usable := len(payload) - len(payload)%2
	out := make([]int16, usable/2)
	for i := range out {
		out[i] = int16(binary.LittleEndian.Uint16(payload[i*2:]))
	}
	return out
}

// publishAudio puts audio on its own channel. The control and spectrum
// events share one channel, and a consumer that watches the spectrum (the
// monitor does) would otherwise take half of the audio away from the
// recorder.
func (c *Client) publishAudio(samples []int16, samplerate int, compressed bool, kind string) {
	chunk := AudioChunk{Samples: samples, Samplerate: samplerate, Sequence: c.sequence, Compressed: compressed, Kind: kind}
	c.sequence++
	event := Event{Type: "audio", Value: chunk}
	select {
	case c.audioEvents <- event:
	default:
		// The recorder is not keeping up; drop the oldest chunk rather
		// than blocking the socket reader.
		select {
		case <-c.audioEvents:
		default:
		}
		select {
		case c.audioEvents <- event:
		default:
		}
	}
}

func (c *Client) publish(event Event) {
	select {
	case c.events <- event:
	default:
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

func (c *Client) readLoop(ctx context.Context, conn *websocket.Conn) {
	defer func() {
		conn.Close()
		// Terminal state: a closed events channel tells consumers the
		// receiver stopped, instead of a frozen spectrum that looks like
		// scanning dead air forever (review H2). readLoop is the only
		// publisher on this channel and it has just exited, so the close
		// cannot race a send. With Reconnect on, reaching this point means
		// the retries are exhausted: the "error" and "reconnecting" events
		// published along the way are the visibility, and the close stays
		// the terminal signal.
		c.eventsOnce.Do(func() { close(c.events) })
	}()
	delay := c.options.ReconnectInitial
	attempts := 0
	for {
		select {
		case <-c.quit:
			return
		case <-ctx.Done():
			return
		default:
		}
		_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		messageType, data, err := conn.ReadMessage()
		if err == nil {
			// The connection works, so the next drop starts from the
			// initial backoff and a fresh attempt budget.
			delay = c.options.ReconnectInitial
			attempts = 0
			if messageType == websocket.BinaryMessage {
				c.handleBinary(data)
			} else {
				c.handleText(string(data))
			}
			continue
		}
		select {
		case <-c.quit:
			return
		case <-ctx.Done():
			return
		default:
		}
		c.mu.Lock()
		c.LastError = err
		c.mu.Unlock()
		c.publish(Event{Type: "error", Value: err})
		attempts++
		if !c.options.Reconnect || attempts > reconnectAttempts {
			// The recorder listens on audioEvents only; without an error
			// there it waits out its whole duration and reports silence.
			select {
			case c.audioEvents <- Event{Type: "error", Value: err}:
			default:
			}
			return
		}
		c.publish(Event{Type: "reconnecting", Value: delay})
		select {
		case <-time.After(delay):
		case <-c.quit:
			return
		case <-ctx.Done():
			return
		}
		delay *= 2
		if delay > c.options.ReconnectMax {
			delay = c.options.ReconnectMax
		}
		dialCtx, dialCancel := context.WithTimeout(ctx, c.options.OpenTimeout)
		dialer := &websocket.Dialer{HandshakeTimeout: 15 * time.Second}
		if !c.options.AllowPrivate {
			dialer.NetDialContext = common.PublicDialContext(15 * time.Second)
		}
		newConn, _, dialErr := dialer.DialContext(dialCtx, c.URL, http.Header{})
		dialCancel()
		if dialErr != nil {
			c.mu.Lock()
			c.LastError = dialErr
			c.mu.Unlock()
			continue
		}
		newConn.SetReadLimit(maxFrameBytes)
		// Reset the decoders before the handshake: a fresh connection
		// restarts ADPCM state and sequence numbers. Profiles and modes
		// belong to the dropped connection: the new server session
		// re-advertises them, and keeping the stale list would both show
		// the caller profiles the server may have changed and shortcut the
		// handshake's own profiles wait (leaving the fresh profiles
		// message to the read loop, which would mistake it for a healthy
		// connection and reset the retry budget).
		c.audio.Reset()
		c.hd.Reset()
		c.fft.Reset()
		c.sequence = 0
		c.mu.Lock()
		c.Profiles = nil
		c.Modes = nil
		c.mu.Unlock()
		if err := c.handshake(ctx, newConn); err != nil {
			newConn.Close()
			c.mu.Lock()
			c.LastError = err
			c.mu.Unlock()
			continue
		}
		conn.Close()
		conn = newConn
		c.mu.Lock()
		c.conn = newConn
		c.mu.Unlock()
	}
}

// Close stops the reader and closes the connection.
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

// Tune retunes the receiver. frequency is in Hz. modulation accepts names such
// as "lsb", "usb", "am" or "nfm" (case-insensitive).
func (c *Client) Tune(ctx context.Context, frequency *int64, modulation string, bandwidth *int64) error {
	c.mu.Lock()
	if frequency != nil {
		c.tuning["frequency"] = *frequency
	}
	if modulation != "" {
		key := strings.ToUpper(modulation)
		if mapped, ok := common.OpenWebRXModes[key]; ok {
			c.tuning["modulation"] = mapped
		} else {
			c.tuning["modulation"] = strings.ToLower(modulation)
		}
	}
	if bandwidth != nil {
		c.tuning["bandwidth"] = *bandwidth
	}
	message := map[string]interface{}{"type": "dspcontrol"}
	for key, value := range c.tuning {
		message[key] = value
	}
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return nil
	}
	payload, _ := json.Marshal(message)
	if err := writeText(conn, payload); err != nil {
		return err
	}
	// Some servers stop the demodulator when the tuning changes and then
	// send no audio at all until the client asks for it again.
	start := map[string]interface{}{"type": "dspcontrol", "action": "start"}
	restart, _ := json.Marshal(start)
	if err := writeText(conn, restart); err != nil {
		return err
	}
	c.drainAudio()
	return nil
}

// drainAudio drops buffered audio from before the current tuning. A retune
// does not flush the socket, so the first chunks after Tune are still the
// PREVIOUS frequency's voice; without this, clip starts bleed the old
// broadcast into every capture (review M4).
func (c *Client) drainAudio() {
	for {
		select {
		case <-c.audioEvents:
			continue
		default:
			return
		}
	}
}

// SelectProfile switches the receiver to another SDR profile. The argument
// must be the raw "sdr_id|profile_id" key (Profile.Key), not the display
// name; the server ignores a name. The demodulator is restarted afterwards.
func (c *Client) SelectProfile(ctx context.Context, profile string) error {
	c.mu.Lock()
	conn := c.conn
	c.profile = profile
	c.mu.Unlock()
	if conn == nil {
		return nil
	}
	message := map[string]interface{}{
		"type":   "selectprofile",
		"params": map[string]interface{}{"profile": profile},
	}
	payload, _ := json.Marshal(message)
	if err := writeText(conn, payload); err != nil {
		return err
	}
	// A profile switch tears down the server's demodulator: no audio flows
	// until the client starts it again, so restart it here. The caller
	// still has to Tune, because the old frequency belongs to the old
	// profile's span.
	restart, _ := json.Marshal(map[string]interface{}{"type": "dspcontrol", "action": "start"})
	if err := writeText(conn, restart); err != nil {
		return err
	}
	c.drainAudio()
	return nil
}

// Profile is one tunable SDR profile a server advertises over the socket.
type Profile struct {
	Key  string // "sdr_id|profile_id", as selectprofile expects
	Name string
}

// ProfileList returns the profiles the server sent after connecting.
func (c *Client) ProfileList() []Profile {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []Profile
	for _, entry := range c.Profiles {
		key, _ := entry["id"].(string)
		name, _ := entry["name"].(string)
		if key != "" {
			out = append(out, Profile{Key: key, Name: name})
		}
	}
	return out
}

// Record collects audio for duration and returns the chunks received. A
// reader failure is returned instead of being reported as clean silence.
func (c *Client) Record(ctx context.Context, duration time.Duration) ([]AudioChunk, error) {
	var chunks []AudioChunk
	deadline := time.NewTimer(duration)
	defer deadline.Stop()
	for {
		select {
		case <-deadline.C:
			return chunks, nil
		case <-ctx.Done():
			return chunks, ctx.Err()
		case event := <-c.audioEvents:
			if event.Type == "audio" {
				chunks = append(chunks, event.Value.(AudioChunk))
			}
			if event.Type == "error" {
				if err, ok := event.Value.(error); ok {
					return chunks, err
				}
			}
		}
	}
}

// DrainAudio discards buffered audio chunks. Call it after Tune: audio
// captured at the old frequency would otherwise pollute the start of the
// next recording.
func (c *Client) DrainAudio() {
	for {
		select {
		case <-c.audioEvents:
		default:
			return
		}
	}
}

// RecordSamples collects audio for duration and returns the concatenated
// samples and the samplerate they were captured at.
func (c *Client) RecordSamples(ctx context.Context, duration time.Duration) ([]int16, int, error) {
	chunks, err := c.Record(ctx, duration)
	if err != nil {
		return nil, 0, err
	}
	var samples []int16
	rate := 12000
	for _, chunk := range chunks {
		samples = append(samples, chunk.Samples...)
		if chunk.Samplerate != 0 {
			rate = chunk.Samplerate
		}
	}
	return samples, rate, nil
}

// WaitAudio blocks until the first audio chunk arrives, the receiver
// reports an error, or timeout expires. It exists so callers can fail a
// tune fast when a server accepts the tuning but streams nothing (a dead
// demodulator or a wedged post-idle connection), instead of waiting out a
// whole recording window on silence. The first chunk is consumed; callers
// lose well under a second of audio, which is the right trade against
// missing a whole pass.
func (c *Client) WaitAudio(ctx context.Context, timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			return fmt.Errorf("no audio within %s of tuning", timeout)
		case <-ctx.Done():
			return ctx.Err()
		case event, ok := <-c.audioEvents:
			if !ok {
				return fmt.Errorf("receiver connection closed before audio")
			}
			if event.Type == "audio" {
				return nil
			}
			if event.Type == "error" {
				if err, ok := event.Value.(error); ok {
					return err
				}
			}
		}
	}
}

// Concat flattens chunks into a single sample slice.
func Concat(chunks []AudioChunk) []int16 {
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
