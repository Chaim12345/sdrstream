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

	"github.com/Chaim12345/sdrstream/go/internal/adpcm"
	"github.com/Chaim12345/sdrstream/go/internal/common"
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

	events     chan Event
	quit       chan struct{}
	conn       *websocket.Conn
	mu         sync.Mutex
	audio      *adpcm.Decoder
	hd         *adpcm.Decoder
	fft        *adpcm.Decoder
	sequence   int
	tuning     map[string]interface{}
	closeOnce  sync.Once
	outputRate int
	hdRate     int
}

// Options configures a Client.
type Options struct {
	OutputRate   int
	HDOutputRate int
	Reconnect    bool
	OpenTimeout  time.Duration
	IdleTimeout  time.Duration
}

// DefaultOptions returns polite defaults.
func DefaultOptions() Options {
	return Options{OutputRate: DefaultOutputRate, HDOutputRate: DefaultHDOutputRate,
		Reconnect: true, OpenTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second}
}

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
	return &Client{
		URL: streamURL, ClientName: "sdrstream",
		events: make(chan Event, 256),
		quit:   make(chan struct{}),
		audio:  adpcm.New(), hd: adpcm.New(), fft: adpcm.New(),
		tuning:     map[string]interface{}{},
		outputRate: options.OutputRate,
		hdRate:     options.HDOutputRate,
	}, nil
}

// Events returns the channel every message is published to.
func (c *Client) Events() <-chan Event { return c.events }

// Connect opens the WebSocket, performs the handshake, starts the DSP and
// returns the server's configuration.
func (c *Client) Connect(ctx context.Context) (Config, error) {
	dialer := &websocket.Dialer{HandshakeTimeout: 15 * time.Second}
	conn, _, err := dialer.DialContext(ctx, c.URL, http.Header{})
	if err != nil {
		return Config{}, err
	}
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
	return c.Config, nil
}

// handshake sends the client greeting, waits for the configuration, then
// sends the pending tuning request and starts the DSP. The handshake completes
// before the reader starts so only one goroutine ever reads from the socket.
func (c *Client) handshake(ctx context.Context, conn *websocket.Conn) error {
	greeting := fmt.Sprintf("SERVER DE CLIENT client=%s type=receiver", c.ClientName)
	if err := conn.WriteMessage(websocket.TextMessage, []byte(greeting)); err != nil {
		return err
	}
	// The configuration arrives as several config messages and only the last
	// one carries the centre frequency, so reading stops once that field is
	// known. A short grace period covers servers that never report it.
	deadline := time.Now().Add(2 * time.Second)
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
			if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
				return err
			}
			continue
		}
		if c.handleText(text) {
			return c.startDSP(ctx, conn)
		}
		if time.Now().After(deadline) {
			return c.startDSP(ctx, conn)
		}
	}
}

// startDSP sends the pending tuning request, then the DSP start request.
func (c *Client) startDSP(ctx context.Context, conn *websocket.Conn) error {
	if len(c.tuning) > 0 {
		message := map[string]interface{}{"type": "dspcontrol"}
		for key, value := range c.tuning {
			message[key] = value
		}
		payload, _ := json.Marshal(message)
		if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
			return err
		}
	}
	start := map[string]interface{}{"type": "dspcontrol", "action": "start"}
	payload, _ := json.Marshal(start)
	return conn.WriteMessage(websocket.TextMessage, payload)
}

func (c *Client) handleText(message string) bool {
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(message), &payload); err != nil {
		return false
	}
	kind, _ := payload["type"].(string)
	value := payload["value"]
	switch kind {
	case "config":
		if fields, ok := value.(map[string]interface{}); ok {
			raw, _ := json.Marshal(fields)
			// Servers send several incremental config messages, so new fields
			// merge into the previous ones instead of replacing them; else
			// audio_compression is lost and ADPCM audio is decoded as raw PCM.
			_ = json.Unmarshal(raw, &c.Config)
		}
		c.publish(Event{Type: "config", Value: c.Config})
		return c.Config.CenterFreq != nil
	case "profiles":
		c.Profiles = toMapSlice(value)
		c.publish(Event{Type: "profiles", Value: c.Profiles})
	case "modes":
		c.Modes = toMapSlice(value)
		c.publish(Event{Type: "modes", Value: c.Modes})
	case "smeter":
		if number, ok := value.(float64); ok {
			c.Smeter = &number
		}
		c.publish(Event{Type: "smeter", Value: value})
	case "receiver_details":
		c.publish(Event{Type: "receiver_details", Value: value})
	case "clients":
		if number, ok := value.(float64); ok {
			c.Clients = int(number)
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

func (c *Client) publishAudio(samples []int16, samplerate int, compressed bool, kind string) {
	chunk := AudioChunk{Samples: samples, Samplerate: samplerate, Sequence: c.sequence, Compressed: compressed, Kind: kind}
	c.sequence++
	c.publish(Event{Type: "audio", Value: chunk})
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
	defer conn.Close()
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
		if err != nil {
			c.LastError = err
			c.publish(Event{Type: "error", Value: err})
			return
		}
		if messageType == websocket.BinaryMessage {
			c.handleBinary(data)
		} else {
			c.handleText(string(data))
		}
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
	return conn.WriteMessage(websocket.TextMessage, payload)
}

// Record collects audio for duration and returns the chunks received.
func (c *Client) Record(ctx context.Context, duration time.Duration) ([]AudioChunk, error) {
	var chunks []AudioChunk
	deadline := time.After(duration)
	for {
		select {
		case <-deadline:
			return chunks, nil
		case <-ctx.Done():
			return chunks, ctx.Err()
		case event := <-c.events:
			if event.Type == "audio" {
				chunks = append(chunks, event.Value.(AudioChunk))
			}
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
