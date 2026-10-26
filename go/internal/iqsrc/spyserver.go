// SpyServer client (Airspy network SDR protocol), implemented from the
// protocol constants and reference-client behavior in SDR++'s
// spyserver_source module (spyserver_protocol.h / spyserver_client.cpp).
// All multi-byte fields are little-endian. Pure Go, no cgo - Android-safe.
package iqsrc

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"sync"

	"github.com/chaim12345/sdrstream/go/internal/common"
	"time"
)

const spyProtocolVersion = (2 << 24) | (0 << 16) | 1700

// Commands (client -> server), 8-byte header {CommandType, BodySize} + body.
const (
	spyCmdHello      = 0
	spyCmdSetSetting = 2
	spyCmdPing       = 3
)

// Settings (SET_SETTING body: {Setting, Value}).
const (
	spySettingStreamingMode    = 0
	spySettingStreamingEnabled = 1
	spySettingGain             = 2
	spySettingIQFormat         = 100
	spySettingIQFrequency      = 101
	spySettingIQDecimation     = 102
	spySettingIQDigitalGain    = 103
)

const (
	spyStreamModeIQOnly = 1

	spyFormatUint8 = 1
	// INT16/INT24/FLOAT exist (2/3/4); only UINT8 is negotiated today.
)

// Message types (server -> client), low 16 bits of MessageType.
const (
	spyMsgDeviceInfo  = 0
	spyMsgClientSync  = 1
	spyMsgPong        = 2
	spyMsgReadSetting = 3
	spyMsgUint8IQ     = 100
)

// DeviceInfo mirrors SpyServerDeviceInfo (52 bytes).
type DeviceInfo struct {
	DeviceType           uint32
	DeviceSerial         uint32
	MaximumSampleRate    uint32
	MaximumBandwidth     uint32
	DecimationStageCount uint32
	GainStageCount       uint32
	MaximumGainIndex     uint32
	MinimumFrequency     uint32
	MaximumFrequency     uint32
	Resolution           uint32
	MinimumIQDecimation  uint32
	ForcedIQFormat       uint32
}

// ClientSync mirrors SpyServerClientSync (40 bytes): the shared-device state
// other clients may also be steering. IQCenterFrequency is the channel center
// we may retune; DeviceCenterFrequency and Gain are shared with all clients.
type ClientSync struct {
	CanControl                uint32
	Gain                      uint32
	DeviceCenterFrequency     uint32
	IQCenterFrequency         uint32
	FFTCenterFrequency        uint32
	MinimumIQCenterFrequency  uint32
	MaximumIQCenterFrequency  uint32
	MinimumFFTCenterFrequency uint32
	MaximumFFTCenterFrequency uint32
}

// SpyConfig selects a SpyServer and channel. SampleRate is the desired channel
// rate; the client picks the largest server rate <= SampleRate (SpyServer only
// offers MaximumSampleRate / 2^d steps). Gain 0 means "leave at server default".
type SpyConfig struct {
	Addr        string
	Frequency   uint32 // Hz, absolute channel center
	SampleRate  uint32 // Hz, desired channel rate (see above)
	Gain        uint32 // gain index 0..MaximumGainIndex; 0 = leave as-is
	DigitalGain bool   // program IQ_DIGITAL_GAIN like the reference client
	AppName     string // sent in HELLO; "sdrstream" if empty
	DialTimeout time.Duration
	// AllowPrivate permits dialing loopback/private hosts (local receivers,
	// tailscale, home LAN). When false the dial is SSRF-guarded like the
	// other monitor sources.
	AllowPrivate bool
}

// SpyClient is one SpyServer connection streaming uint8 I/Q.
type SpyClient struct {
	conn    net.Conn
	Info    DeviceInfo
	Sync    ClientSync
	rate    uint32
	mu      sync.Mutex
	closed  bool
	readBuf []byte
}

// DialSpy connects, handshakes, waits for DEVICE_INFO/CLIENT_SYNC, programs
// the channel, and enables IQ-only streaming.
func DialSpy(ctx context.Context, cfg SpyConfig) (*SpyClient, error) {
	if cfg.Addr == "" {
		return nil, errors.New("iqsrc: empty spyserver address")
	}
	if cfg.AppName == "" {
		cfg.AppName = "sdrstream"
	}
	to := cfg.DialTimeout
	if to == 0 {
		to = 10 * time.Second
	}
	d := net.Dialer{Timeout: to}
	var conn net.Conn
	var err error
	if cfg.AllowPrivate {
		conn, err = d.DialContext(ctx, "tcp", cfg.Addr)
	} else {
		conn, err = common.DialPublic(ctx, &d, "tcp", cfg.Addr)
	}
	if err != nil {
		return nil, fmt.Errorf("iqsrc: dial spyserver %s: %w", cfg.Addr, err)
	}
	c := &SpyClient{conn: conn}

	// HELLO: protocol version + app name.
	app := []byte(cfg.AppName)
	body := make([]byte, 4+len(app))
	binary.LittleEndian.PutUint32(body, spyProtocolVersion)
	copy(body[4:], app)
	if err := c.command(spyCmdHello, body); err != nil {
		conn.Close()
		return nil, err
	}

	// Await DEVICE_INFO and CLIENT_SYNC (arrive in either order, possibly
	// interleaved with READ_SETTING/PONG).
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	haveInfo, haveSync := false, false
	for !(haveInfo && haveSync) {
		mtype, mbody, err := c.readMessage()
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("iqsrc: handshake: %w", err)
		}
		switch mtype {
		case spyMsgDeviceInfo:
			if len(mbody) < 52 {
				conn.Close()
				return nil, fmt.Errorf("iqsrc: short DEVICE_INFO (%d bytes)", len(mbody))
			}
			var di DeviceInfo
			for i, fp := range []*uint32{&di.DeviceType, &di.DeviceSerial, &di.MaximumSampleRate, &di.MaximumBandwidth, &di.DecimationStageCount, &di.GainStageCount, &di.MaximumGainIndex, &di.MinimumFrequency, &di.MaximumFrequency, &di.Resolution, &di.MinimumIQDecimation, &di.ForcedIQFormat} {
				*fp = binary.LittleEndian.Uint32(mbody[4*i:])
			}
			c.Info = di
			haveInfo = true
		case spyMsgClientSync:
			if len(mbody) < 40 {
				conn.Close()
				return nil, fmt.Errorf("iqsrc: short CLIENT_SYNC (%d bytes)", len(mbody))
			}
			var cs ClientSync
			for i, fp := range []*uint32{&cs.CanControl, &cs.Gain, &cs.DeviceCenterFrequency, &cs.IQCenterFrequency, &cs.FFTCenterFrequency, &cs.MinimumIQCenterFrequency, &cs.MaximumIQCenterFrequency, &cs.MinimumFFTCenterFrequency, &cs.MaximumFFTCenterFrequency} {
				*fp = binary.LittleEndian.Uint32(mbody[4*i:])
			}
			c.Sync = cs
			haveSync = true
		}
	}
	conn.SetReadDeadline(time.Time{})

	// Choose decimation: largest rate <= requested (rates are MaxSampleRate/2^d).
	rate := c.Info.MaximumSampleRate >> c.Info.MinimumIQDecimation
	decim := c.Info.MinimumIQDecimation
	for d := c.Info.MinimumIQDecimation; d <= c.Info.DecimationStageCount; d++ {
		r := c.Info.MaximumSampleRate / (1 << d)
		if cfg.SampleRate > 0 && r > cfg.SampleRate {
			continue
		}
		rate, decim = r, d
		break
	}
	c.rate = rate

	if cfg.Frequency == 0 {
		cfg.Frequency = c.Sync.IQCenterFrequency
	}
	type kv struct{ k, v uint32 }
	settings := []kv{
		{spySettingStreamingMode, spyStreamModeIQOnly},
		{spySettingIQFormat, spyFormatUint8},
		{spySettingIQFrequency, cfg.Frequency},
		{spySettingIQDecimation, decim},
	}
	if cfg.Gain > 0 {
		g := cfg.Gain
		if g > c.Info.MaximumGainIndex {
			g = c.Info.MaximumGainIndex
		}
		settings = append(settings, kv{spySettingGain, g})
		if cfg.DigitalGain {
			// Reference-client formula (SDR++): device-dependent.
			var dg float64
			switch c.Info.DeviceType {
			case 1: // AIRSPY_ONE
				dg = float64(c.Info.MaximumGainIndex-g) + float64(decim)*3.01
			default: // AIRSPY_HF, RTLSDR
				dg = float64(decim) * 3.01
			}
			settings = append(settings, kv{spySettingIQDigitalGain, uint32(dg)})
		}
	}
	var sb [8]byte
	for _, s := range settings {
		binary.LittleEndian.PutUint32(sb[0:4], s.k)
		binary.LittleEndian.PutUint32(sb[4:8], s.v)
		if err := c.command(spyCmdSetSetting, sb[:]); err != nil {
			conn.Close()
			return nil, err
		}
	}
	binary.LittleEndian.PutUint32(sb[0:4], spySettingStreamingEnabled)
	binary.LittleEndian.PutUint32(sb[4:8], 1)
	if err := c.command(spyCmdSetSetting, sb[:]); err != nil {
		conn.Close()
		return nil, err
	}
	return c, nil
}

// SampleRate is the negotiated channel rate (MaximumSampleRate / 2^decimation).
func (c *SpyClient) SampleRate() uint32 { return c.rate }

func (c *SpyClient) command(cmd uint32, body []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("iqsrc: spyserver closed")
	}
	var hdr [8]byte
	binary.LittleEndian.PutUint32(hdr[0:4], cmd)
	binary.LittleEndian.PutUint32(hdr[4:8], uint32(len(body)))
	if _, err := c.conn.Write(append(hdr[:], body...)); err != nil {
		return fmt.Errorf("iqsrc: spyserver command %d: %w", cmd, err)
	}
	return nil
}

// readMessage reads one 20-byte message header + body. Returns the low 16
// bits of MessageType (the message type; high bits are per-message flags)
// and the body. The flags are returned via out param when non-nil.
func (c *SpyClient) readMessage() (uint32, []byte, error) {
	mtype, _, body, err := c.readMessageFlags()
	return mtype, body, err
}

func (c *SpyClient) readMessageFlags() (uint32, uint32, []byte, error) {
	var hdr [20]byte
	if _, err := io.ReadFull(c.conn, hdr[:]); err != nil {
		return 0, 0, nil, err
	}
	size := binary.LittleEndian.Uint32(hdr[16:20])
	if size > 1<<20 {
		return 0, 0, nil, fmt.Errorf("iqsrc: spyserver message too large: %d", size)
	}
	body := make([]byte, size)
	if _, err := io.ReadFull(c.conn, body); err != nil {
		return 0, 0, nil, err
	}
	mt := binary.LittleEndian.Uint32(hdr[4:8])
	return mt & 0xFFFF, mt >> 16, body, nil
}

// ReadSamples fills buf from the next UINT8 IQ message, transparently
// skipping sync/setting/keepalive messages. Samples are normalized per the
// reference client: (v-128) / (128 * 10^(flags/20)).
func (c *SpyClient) ReadSamples(buf []complex64) (int, error) {
	for {
		mtype, flags, body, err := c.readMessageFlags()
		if err != nil {
			return 0, err
		}
		switch mtype {
		case spyMsgUint8IQ:
			n := len(body) / 2
			if n > len(buf) {
				n = len(buf)
			}
			scale := float32(1.0 / (128.0 * math.Pow(10, float64(flags)/20.0)))
			for i := 0; i < n; i++ {
				buf[i] = complex((float32(body[2*i])-128)*scale, (float32(body[2*i+1])-128)*scale)
			}
			return n, nil
		case spyMsgPong, spyMsgReadSetting, spyMsgClientSync:
			continue
		default:
			continue // FFT/status/other streams: not subscribed, tolerate
		}
	}
}

// SetFrequency retunes the channel center on a live connection.
func (c *SpyClient) SetFrequency(hz uint32) error {
	var b [8]byte
	binary.LittleEndian.PutUint32(b[0:4], spySettingIQFrequency)
	binary.LittleEndian.PutUint32(b[4:8], hz)
	return c.command(spyCmdSetSetting, b[:])
}

// Close disables streaming and ends the connection.
func (c *SpyClient) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	var msg [16]byte
	binary.LittleEndian.PutUint32(msg[0:4], spyCmdSetSetting)
	binary.LittleEndian.PutUint32(msg[4:8], 8)
	binary.LittleEndian.PutUint32(msg[8:12], spySettingStreamingEnabled)
	// best-effort streaming-off; ignore write error
	c.conn.Write(msg[:])
	c.mu.Unlock()
	return c.conn.Close()
}
