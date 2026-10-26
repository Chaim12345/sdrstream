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

// DeviceInfo mirrors SpyServerDeviceInfo (48 bytes, 12 uint32 fields).
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
	conn net.Conn
	Info DeviceInfo
	// HaveSync reports whether the server volunteered CLIENT_SYNC during the
	// handshake. The reference client never requires it; when false, Sync is
	// zero-valued and its facts (CanControl, center frequencies) are UNKNOWN,
	// not zero.
	HaveSync bool
	Sync     ClientSync
	rate    uint32
	mu      sync.Mutex
	closed  bool
	readBuf []byte
	// Stream accounting, written by the single ReadSamples consumer and
	// read via Stats. Sequence semantics are as observed on the wire (per
	// IQ message, incrementing); gap counting treats any discontinuity as
	// lost-or-reordered messages without claiming which.
	iqMsgs      uint64
	iqSeqGaps   uint64
	lastIQSeq   int64
	skippedMsgs uint64
	// settingsEcho holds the server's READ_SETTING echoes (setting ->
	// applied value), the closest thing the protocol has to an ACK.
	settingsEcho map[uint32]uint32
}

// SettingEcho returns the server's last echoed applied value for a setting.
func (c *SpyClient) SettingEcho(setting uint32) (uint32, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.settingsEcho[setting]
	return v, ok
}

// SyncSnapshot returns the latest server-reported sync state (updated on
// every CLIENT_SYNC, not only at handshake).
func (c *SpyClient) SyncSnapshot() (ClientSync, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Sync, c.HaveSync
}

// SpyStats is a point-in-time snapshot of stream accounting.
type SpyStats struct {
	IQMessages   uint64
	IQSeqGaps    uint64 // count of sequence discontinuities between IQ messages (lost, reordered, or server restart; cause not claimed)
	SkippedNonIQ uint64 // PONG/READ_SETTING/CLIENT_SYNC/other messages consumed while waiting for IQ
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
	c := &SpyClient{conn: conn, lastIQSeq: -1}

	// HELLO: protocol version + app name.
	app := []byte(cfg.AppName)
	body := make([]byte, 4+len(app))
	binary.LittleEndian.PutUint32(body, spyProtocolVersion)
	copy(body[4:], app)
	if err := c.command(spyCmdHello, body); err != nil {
		conn.Close()
		return nil, err
	}

	// Await DEVICE_INFO (required, as in the reference client, which waits
	// 3s for it and nothing else) and CLIENT_SYNC (optional: the reference
	// never requires it, so a server that omits it must not fail the
	// handshake). Once DEVICE_INFO arrives, allow a short grace for
	// CLIENT_SYNC, then proceed with HaveSync=false.
	deadline := time.Now().Add(5 * time.Second)
	const syncGrace = 500 * time.Millisecond
	conn.SetReadDeadline(deadline)
	haveInfo, haveSync := false, false
	msgs := 0
	for !(haveInfo && (haveSync || time.Now().After(deadline))) {
		mtype, mbody, err := c.readMessage()
		if err != nil {
			if haveInfo && !haveSync && isTimeout(err) {
				break // proceed without CLIENT_SYNC
			}
			conn.Close()
			if msgs == 0 && isTimeout(err) {
				return nil, fmt.Errorf("iqsrc: handshake: no response from server within 5s (0 messages received)")
			}
			return nil, fmt.Errorf("iqsrc: handshake: %w (after %d messages, device_info=%v client_sync=%v)", err, msgs, haveInfo, haveSync)
		}
		msgs++
		switch mtype {
		case spyMsgDeviceInfo:
			// Older servers trail the reference struct (48 bytes / 12
			// fields today); parse the fields that arrived, leave the
			// rest zero. 32 bytes covers through MinimumFrequency, the
			// smallest shape still useful.
			if len(mbody) < 32 {
				conn.Close()
				return nil, fmt.Errorf("iqsrc: short DEVICE_INFO (%d bytes)", len(mbody))
			}
			var di DeviceInfo
			fields := []*uint32{&di.DeviceType, &di.DeviceSerial, &di.MaximumSampleRate, &di.MaximumBandwidth, &di.DecimationStageCount, &di.GainStageCount, &di.MaximumGainIndex, &di.MinimumFrequency, &di.MaximumFrequency, &di.Resolution, &di.MinimumIQDecimation, &di.ForcedIQFormat}
			for i, fp := range fields {
				if 4*i+4 > len(mbody) {
					break
				}
				*fp = binary.LittleEndian.Uint32(mbody[4*i:])
			}
			c.Info = di
			haveInfo = true
			// CLIENT_SYNC, if the server volunteers one, follows promptly;
			// do not hold the whole handshake deadline open for it.
			if grace := time.Now().Add(syncGrace); grace.Before(deadline) {
				conn.SetReadDeadline(grace)
			}
		case spyMsgClientSync:
			// Same trailing-extension tolerance: 40 bytes / 10 fields
			// today, older servers observed at 36. 20 bytes covers
			// through FFTCenterFrequency.
			if len(mbody) < 20 {
				conn.Close()
				return nil, fmt.Errorf("iqsrc: short CLIENT_SYNC (%d bytes)", len(mbody))
			}
			var cs ClientSync
			fields := []*uint32{&cs.CanControl, &cs.Gain, &cs.DeviceCenterFrequency, &cs.IQCenterFrequency, &cs.FFTCenterFrequency, &cs.MinimumIQCenterFrequency, &cs.MaximumIQCenterFrequency, &cs.MinimumFFTCenterFrequency, &cs.MaximumFFTCenterFrequency}
			for i, fp := range fields {
				if 4*i+4 > len(mbody) {
					break
				}
				*fp = binary.LittleEndian.Uint32(mbody[4*i:])
			}
			c.Sync = cs
			haveSync = true
		}
	}
	conn.SetReadDeadline(time.Time{})
	c.HaveSync = haveSync

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
		if !c.HaveSync {
			conn.Close()
			return nil, fmt.Errorf("iqsrc: spyserver: no frequency configured and server sent no CLIENT_SYNC to inherit one from")
		}
		cfg.Frequency = c.Sync.IQCenterFrequency
	}
	// Settings in the reference client's order (SDR++ start(): format,
	// decimation, frequency, mode, gain, digital gain, then enable). A
	// previous order sent STREAMING_MODE first; some servers may apply or
	// lock the channel pipeline at that point, silently ignoring a later
	// frequency. Order parity removes that variable.
	type kv struct{ k, v uint32 }
	settings := []kv{
		{spySettingIQFormat, spyFormatUint8},
		{spySettingIQDecimation, decim},
		{spySettingIQFrequency, cfg.Frequency},
		{spySettingStreamingMode, spyStreamModeIQOnly},
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

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// readMessage reads one 20-byte message header + body. Returns the low 16
// bits of MessageType (the message type; high bits are per-message flags)
// and the body. The flags are returned via out param when non-nil.
func (c *SpyClient) readMessage() (uint32, []byte, error) {
	mtype, _, _, body, err := c.readMessageFlags()
	return mtype, body, err
}

func (c *SpyClient) readMessageFlags() (uint32, uint32, uint32, []byte, error) {
	var hdr [20]byte
	if _, err := io.ReadFull(c.conn, hdr[:]); err != nil {
		return 0, 0, 0, nil, err
	}
	size := binary.LittleEndian.Uint32(hdr[16:20])
	if size > 1<<20 {
		return 0, 0, 0, nil, fmt.Errorf("iqsrc: spyserver message too large: %d", size)
	}
	body := make([]byte, size)
	if _, err := io.ReadFull(c.conn, body); err != nil {
		return 0, 0, 0, nil, err
	}
	mt := binary.LittleEndian.Uint32(hdr[4:8])
	seq := binary.LittleEndian.Uint32(hdr[12:16])
	return mt & 0xFFFF, mt >> 16, seq, body, nil
}

// Stats snapshots the stream accounting.
func (c *SpyClient) Stats() SpyStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return SpyStats{IQMessages: c.iqMsgs, IQSeqGaps: c.iqSeqGaps, SkippedNonIQ: c.skippedMsgs}
}

// ReadSamples fills buf from the next UINT8 IQ message, transparently
// skipping sync/setting/keepalive messages. Samples are normalized per the
// reference client: (v-128) / (128 * 10^(flags/20)).
func (c *SpyClient) ReadSamples(buf []complex64) (int, error) {
	for {
		mtype, flags, seq, body, err := c.readMessageFlags()
		if err != nil {
			return 0, err
		}
		switch mtype {
		case spyMsgUint8IQ:
			c.mu.Lock()
			if c.lastIQSeq >= 0 && seq != uint32(c.lastIQSeq)+1 {
				c.iqSeqGaps++ // discontinuity event; direction/cause not claimed
			}
			c.lastIQSeq = int64(seq)
			c.iqMsgs++
			c.mu.Unlock()
			n := len(body) / 2
			if n > len(buf) {
				n = len(buf)
			}
			scale := float32(1.0 / (128.0 * math.Pow(10, float64(flags)/20.0)))
			for i := 0; i < n; i++ {
				buf[i] = complex((float32(body[2*i])-128)*scale, (float32(body[2*i+1])-128)*scale)
			}
			return n, nil
		case spyMsgClientSync:
			// Mid-stream sync: the server is reporting applied state
			// (e.g. the center frequency it actually uses). Keep c.Sync
			// current so callers can read back control truth instead of
			// trusting what they requested.
			if len(body) >= 20 {
				c.mu.Lock()
				fields := []*uint32{&c.Sync.CanControl, &c.Sync.Gain, &c.Sync.DeviceCenterFrequency, &c.Sync.IQCenterFrequency, &c.Sync.FFTCenterFrequency, &c.Sync.MinimumIQCenterFrequency, &c.Sync.MaximumIQCenterFrequency, &c.Sync.MinimumFFTCenterFrequency, &c.Sync.MaximumFFTCenterFrequency}
				for i, fp := range fields {
					if 4*i+4 > len(body) {
						break
					}
					*fp = binary.LittleEndian.Uint32(body[4*i:])
				}
				c.HaveSync = true
				c.skippedMsgs++
				c.mu.Unlock()
			}
			continue
		case spyMsgReadSetting:
			if len(body) >= 8 {
				c.mu.Lock()
				if c.settingsEcho == nil {
					c.settingsEcho = map[uint32]uint32{}
				}
				c.settingsEcho[binary.LittleEndian.Uint32(body[0:4])] = binary.LittleEndian.Uint32(body[4:8])
				c.skippedMsgs++
				c.mu.Unlock()
			}
			continue
		case spyMsgPong:
			c.mu.Lock()
			c.skippedMsgs++
			c.mu.Unlock()
			continue
		default:
			c.mu.Lock()
			c.skippedMsgs++
			c.mu.Unlock()
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
