package iqsrc

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// fakeSpy speaks the server half of the SpyServer protocol: captures HELLO and
// SET_SETTING commands, answers with DEVICE_INFO + CLIENT_SYNC, then streams
// one UINT8 IQ message per streamTicks call.
type fakeSpy struct {
	ln       net.Listener
	hello    []byte
	mu       sync.Mutex
	settings [][2]uint32
	stream   chan []byte   // raw IQ bodies to send, one message each
	enabled  chan struct{} // closed when STREAMING_ENABLED=1 is recorded
	info     DeviceInfo
	sync     ClientSync
	// syncBodyLen shrinks the CLIENT_SYNC body to simulate older servers;
	// 0 means the full 40-byte current shape.
	syncBodyLen int
}

func newFakeSpy(t *testing.T) *fakeSpy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSpy{ln: ln, stream: make(chan []byte, 4), enabled: make(chan struct{})}
	f.info = DeviceInfo{DeviceType: 3, DeviceSerial: 0xBEEF, MaximumSampleRate: 10000000, MaximumBandwidth: 8000000, DecimationStageCount: 5, GainStageCount: 22, MaximumGainIndex: 21, MinimumFrequency: 1000, MaximumFrequency: 1800000000, Resolution: 12, MinimumIQDecimation: 2, ForcedIQFormat: 0}
	f.sync = ClientSync{CanControl: 1, Gain: 10, DeviceCenterFrequency: 145000000, IQCenterFrequency: 144800000}
	go f.serve()
	return f
}

func putMsg(buf []byte, mtype, stream, seq uint32, body []byte) {
	binary.LittleEndian.PutUint32(buf[0:4], spyProtocolVersion)
	binary.LittleEndian.PutUint32(buf[4:8], mtype)
	binary.LittleEndian.PutUint32(buf[8:12], stream)
	binary.LittleEndian.PutUint32(buf[12:16], seq)
	binary.LittleEndian.PutUint32(buf[16:20], uint32(len(body)))
}

func (f *fakeSpy) serve() {
	conn, err := f.ln.Accept()
	if err != nil {
		return
	}
	defer conn.Close()
	// Read HELLO.
	var hdr [8]byte
	if _, err := io.ReadFull(conn, hdr[:]); err != nil {
		return
	}
	hs := binary.LittleEndian.Uint32(hdr[4:8])
	hello := make([]byte, hs)
	io.ReadFull(conn, hello)
	f.hello = hello
	// Send DEVICE_INFO then CLIENT_SYNC.
	var msg [72]byte
	di := make([]byte, 48)
	for i, v := range []uint32{f.info.DeviceType, f.info.DeviceSerial, f.info.MaximumSampleRate, f.info.MaximumBandwidth, f.info.DecimationStageCount, f.info.GainStageCount, f.info.MaximumGainIndex, f.info.MinimumFrequency, f.info.MaximumFrequency, f.info.Resolution, f.info.MinimumIQDecimation, f.info.ForcedIQFormat} {
		binary.LittleEndian.PutUint32(di[4*i:], v)
	}
	putMsg(msg[:20], spyMsgDeviceInfo, 0, 0, di)
	conn.Write(append(msg[:20], di...))
	bodyLen := f.syncBodyLen
	if bodyLen == 0 {
		bodyLen = 40
	}
	cs := make([]byte, bodyLen)
	for i, v := range []uint32{f.sync.CanControl, f.sync.Gain, f.sync.DeviceCenterFrequency, f.sync.IQCenterFrequency, f.sync.FFTCenterFrequency, f.sync.MinimumIQCenterFrequency, f.sync.MaximumIQCenterFrequency, f.sync.MinimumFFTCenterFrequency, f.sync.MaximumFFTCenterFrequency} {
		binary.LittleEndian.PutUint32(cs[4*i:], v)
	}
	putMsg(msg[:20], spyMsgClientSync, 0, 1, cs)
	conn.Write(append(msg[:20], cs...))
	// Read commands; on STREAMING_ENABLED=1 start draining the stream channel.
	for {
		if _, err := io.ReadFull(conn, hdr[:]); err != nil {
			return
		}
		cmd := binary.LittleEndian.Uint32(hdr[0:4])
		bs := binary.LittleEndian.Uint32(hdr[4:8])
		body := make([]byte, bs)
		io.ReadFull(conn, body)
		if cmd == spyCmdSetSetting && bs == 8 {
			f.mu.Lock()
			f.settings = append(f.settings, [2]uint32{binary.LittleEndian.Uint32(body[0:4]), binary.LittleEndian.Uint32(body[4:8])})
			last := f.settings[len(f.settings)-1]
			f.mu.Unlock()
			if last == [2]uint32{spySettingStreamingEnabled, 1} {
				close(f.enabled)
				go func() {
					for b := range f.stream {
						m := make([]byte, 20+len(b))
						putMsg(m[:20], spyMsgUint8IQ|(20<<16), 1, 2, b)
						copy(m[20:], b)
						conn.SetWriteDeadline(time.Now().Add(time.Second))
						if _, err := conn.Write(m); err != nil {
							return
						}
					}
				}()
			}
		}
	}
}

func (f *fakeSpy) close() { close(f.stream); f.ln.Close() }

func (f *fakeSpy) settingsCopy() [][2]uint32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][2]uint32, len(f.settings))
	copy(out, f.settings)
	return out
}

func TestSpyHandshakeAndSettings(t *testing.T) {
	f := newFakeSpy(t)
	defer f.close()
	c, err := DialSpy(context.Background(), SpyConfig{
		Addr: f.ln.Addr().String(), Frequency: 144800000, SampleRate: 1200000,
		Gain: 15, DigitalGain: true, AppName: "sdrstream-test", AllowPrivate: true})
	if err != nil {
		t.Fatalf("DialSpy: %v", err)
	}
	defer c.Close()
	// HELLO: version + app name.
	if len(f.hello) != 4+len("sdrstream-test") {
		t.Fatalf("hello len %d", len(f.hello))
	}
	if got := binary.LittleEndian.Uint32(f.hello); got != spyProtocolVersion {
		t.Fatalf("hello version %#x", got)
	}
	if string(f.hello[4:]) != "sdrstream-test" {
		t.Fatalf("hello app %q", f.hello[4:])
	}
	<-f.enabled
	if c.Info.DeviceSerial != 0xBEEF || c.Sync.IQCenterFrequency != 144800000 {
		t.Fatalf("info/sync parse: %+v %+v", c.Info, c.Sync)
	}
	// Decimation: rates are 10M/2^d for d in [2,5] = 2500k/1250k/625k/312.5k.
	// Largest <= 1.2M is 625k at d=4.
	if c.SampleRate() != 625000 {
		t.Fatalf("rate %d want 625000", c.SampleRate())
	}
	want := [][2]uint32{
		{spySettingStreamingMode, spyStreamModeIQOnly},
		{spySettingIQFormat, spyFormatUint8},
		{spySettingIQFrequency, 144800000},
		{spySettingIQDecimation, 4},
		{spySettingGain, 15},
		{spySettingIQDigitalGain, 12}, // RTLSDR: uint32(4*3.01)=12
		{spySettingStreamingEnabled, 1},
	}
	got := f.settingsCopy()
	if len(got) != len(want) {
		t.Fatalf("settings %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("setting %d: got %v want %v (all %v)", i, got[i], want[i], got)
		}
	}
}

func TestSpyReadSamples(t *testing.T) {
	f := newFakeSpy(t)
	defer f.close()
	c, err := DialSpy(context.Background(), SpyConfig{Addr: f.ln.Addr().String(), Frequency: 1000000, SampleRate: 1000000, AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// flags=20 -> gain factor 10^(20/20)=10, so values are divided by 1280.
	f.stream <- []byte{128, 128, 255, 0}
	buf := make([]complex64, 2)
	n, err := c.ReadSamples(buf)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("n=%d", n)
	}
	if real(buf[0]) != 0 || imag(buf[0]) != 0 {
		t.Fatalf("midpoint: %v", buf[0])
	}
	wantR := float32(127.0 / 1280.0)
	wantI := float32(-128.0 / 1280.0)
	if d := real(buf[1]) - wantR; d > 1e-6 || d < -1e-6 {
		t.Fatalf("real %v want %v", real(buf[1]), wantR)
	}
	if d := imag(buf[1]) - wantI; d > 1e-6 || d < -1e-6 {
		t.Fatalf("imag %v want %v", imag(buf[1]), wantI)
	}
}

func TestSpyRetuneAndClose(t *testing.T) {
	f := newFakeSpy(t)
	defer f.close()
	c, err := DialSpy(context.Background(), SpyConfig{Addr: f.ln.Addr().String(), Frequency: 1000000, SampleRate: 1000000, AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SetFrequency(435000000); err != nil {
		t.Fatalf("SetFrequency: %v", err)
	}
	found := false
	for i := 0; i < 100 && !found; i++ {
		for _, s := range f.settingsCopy() {
			if s == [2]uint32{spySettingIQFrequency, 435000000} {
				found = true
			}
		}
		if !found {
			time.Sleep(5 * time.Millisecond)
		}
	}
	if !found {
		t.Fatalf("retune command not recorded: %v", f.settingsCopy())
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.SetFrequency(1); err == nil {
		t.Fatal("SetFrequency after Close succeeded")
	}
}

// TestSpyHandshakeToleratesOlderServerShapes pins what live public servers
// actually send: 48-byte DEVICE_INFO is current, but older servers answer
// with a 36-byte CLIENT_SYNC (9 fields). The handshake must accept the
// trailing-extension shape and leave missing fields zero.
func TestSpyHandshakeToleratesOlderServerShapes(t *testing.T) {
	f := newFakeSpy(t)
	defer f.close()
	f.syncBodyLen = 36
	c, err := DialSpy(context.Background(), SpyConfig{Addr: f.ln.Addr().String(), Frequency: 1000000, SampleRate: 1000000, AllowPrivate: true})
	if err != nil {
		t.Fatalf("DialSpy against older 36-byte CLIENT_SYNC server: %v", err)
	}
	defer c.Close()
	if c.Sync.MaximumFFTCenterFrequency != 0 {
		t.Fatalf("missing trailing field should be zero, got %d", c.Sync.MaximumFFTCenterFrequency)
	}
	if c.Sync.CanControl == 0 && c.Sync.IQCenterFrequency == 0 {
		t.Fatal("present fields not parsed")
	}
}
