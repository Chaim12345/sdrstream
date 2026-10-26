package main

import (
	"encoding/json"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chaim12345/sdrstream/go/cmd/rxprobe/pcmv4/v4enc"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{}

// fakeOpenWebRX serves a scripted websocket endpoint: optional greeting
// banner, then behaviour per mode.
func fakeOpenWebRX(t *testing.T, mode string) (*httptest.Server, websocket.Dialer) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := upgrader.Upgrade(w, req, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		// read the client greeting
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
		switch mode {
		case "silent":
			time.Sleep(8 * time.Second) // never send: probe must end on its 5s deadline without panic
		case "config":
			conn.WriteMessage(websocket.TextMessage, []byte("CLIENT DE SERVER openwebrx"))
			conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"config","params":{"center_freq":144800000}}`))
			time.Sleep(8 * time.Second)
		case "close":
			conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "bye"))
			time.Sleep(time.Second)
		}
	}))
	return srv, websocket.Dialer{HandshakeTimeout: 3 * time.Second}
}

func runFake(t *testing.T, mode, want string) result {
	t.Helper()
	srv, d := runFakeServer(t, mode)
	defer srv.Close()
	c := candidate{EndpointKey: "127.0.0.1:0", URL: "http" + strings.TrimPrefix(srv.URL, "http"), Family: "OpenWebRX"}
	var r result
	probeOpenWebRXWithDialer(c, &r, d)
	if r.Outcome != want {
		t.Fatalf("mode %s: outcome=%q want %q (err=%s msgs=%d)", mode, r.Outcome, want, r.Err, r.Msgs)
	}
	return r
}

func runFakeServer(t *testing.T, mode string) (*httptest.Server, websocket.Dialer) { return fakeOpenWebRX(t, mode) }

func TestProbeSilentServerNoPanic(t *testing.T) {
	// The old harness panicked here: read timeout poisoned the connection
	// and the retry read panicked (gorilla conn.go:1030).
	r := runFake(t, "silent", "connected-no-data")
	if r.Msgs != 0 {
		t.Fatalf("silent server produced %d msgs", r.Msgs)
	}
}

func TestProbeConfigHandshake(t *testing.T) {
	r := runFake(t, "config", "handshake-complete-config")
	if !strings.Contains(r.Facts, "center_freq") {
		t.Fatalf("config facts missing: %q", r.Facts)
	}
}

func TestProbeServerClose(t *testing.T) {
	// Clean close after zero messages: break on error, no panic, no retry.
	runFake(t, "close", "connected-no-data")
}

// fakeSpyServer speaks just enough SpyServer protocol for the stage-2 probe.
func fakeSpyServer(t *testing.T, streamIQ bool) (addr string, stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil { t.Fatal(err) }
	go func() {
		conn, err := ln.Accept()
		if err != nil { return }
		defer conn.Close()
		msg := func(mt uint32, body []byte) []byte {
			b := make([]byte, 20+len(body))
			binary.LittleEndian.PutUint32(b[4:8], mt)
			binary.LittleEndian.PutUint32(b[12:16], 0)
			binary.LittleEndian.PutUint32(b[16:20], uint32(len(body)))
			copy(b[20:], body)
			return b
		}
		// read HELLO (8-byte cmd header + body)
		var hdr [8]byte
		if _, err := io.ReadFull(conn, hdr[:]); err != nil { return }
		body := make([]byte, binary.LittleEndian.Uint32(hdr[4:8]))
		io.ReadFull(conn, body)
		// DEVICE_INFO (48B) + CLIENT_SYNC (36B: cancontrol=1, iq_center=87.7MHz)
		conn.Write(msg(0, make([]byte, 48)))
		sync := make([]byte, 36)
		binary.LittleEndian.PutUint32(sync[0:4], 1)
		binary.LittleEndian.PutUint32(sync[8:12], 87700000)
		binary.LittleEndian.PutUint32(sync[12:16], 87700000)
		conn.Write(msg(1, sync))
		// read SETTING enable
		if _, err := io.ReadFull(conn, hdr[:]); err != nil { return }
		body = make([]byte, binary.LittleEndian.Uint32(hdr[4:8]))
		io.ReadFull(conn, body)
		if streamIQ {
			for seq := uint32(0); seq < 3; seq++ {
				b := msg(100, make([]byte, 2048))
				binary.LittleEndian.PutUint32(b[12:16], seq)
				conn.Write(b)
			}
		}
		time.Sleep(8 * time.Second) // hold connection open; probe ends on its 5s deadline
	}()
	return ln.Addr().String(), func() { ln.Close() }
}

func TestSpyStage2ReceivesIQ(t *testing.T) {
	addr, stop := fakeSpyServer(t, true)
	defer stop()
	dial := func(ctx context.Context, a string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", a)
	}
	var r result
	probeSpyStage2Dial(candidate{EndpointKey: addr, Family: "SpyServer"}, &r, dial)
	if r.Outcome != "iq-samples-received" {
		t.Fatalf("outcome=%q err=%s facts=%s", r.Outcome, r.Err, r.Facts)
	}
	if !strings.Contains(r.Facts, "iq_center_hz=87700000") || !strings.Contains(r.Facts, "iq_msgs=3") {
		t.Fatalf("facts=%s", r.Facts)
	}
}

func TestSpyStage2NoStream(t *testing.T) {
	addr, stop := fakeSpyServer(t, false)
	defer stop()
	dial := func(ctx context.Context, a string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", a)
	}
	var r result
	probeSpyStage2Dial(candidate{EndpointKey: addr, Family: "SpyServer"}, &r, dial)
	if r.Outcome != "no-stream-after-enable" {
		t.Fatalf("outcome=%q err=%s facts=%s", r.Outcome, r.Err, r.Facts)
	}
}

// fakeKiwi serves a minimal KiwiSDR SND websocket: accepts the SET lines,
// optionally announces load_cfg with init.freq, and streams IQ SND frames
// only after "SET mod=iq".
func fakeKiwi(t *testing.T, withInitFreq bool) (*httptest.Server, websocket.Dialer, *int32) {
	t.Helper()
	var gotTune int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := upgrader.Upgrade(w, req, nil)
		if err != nil { return }
		defer conn.Close()
		if withInitFreq {
			cfg := url.QueryEscape(`{"init":{"freq":7020.0,"mode":"lsb"}}`)
			conn.WriteMessage(websocket.TextMessage, []byte("MSG audio_rate=12000 load_cfg="+cfg))
		} else {
			conn.WriteMessage(websocket.TextMessage, []byte("MSG audio_rate=12000"))
		}
		for {
			mt, data, err := conn.ReadMessage()
			if err != nil { return }
			if mt != websocket.TextMessage { continue }
			s := string(data)
			if strings.HasPrefix(s, "SET mod=") {
				atomic.StoreInt32(&gotTune, 1)
				if !strings.Contains(s, "freq=7020.000") {
					atomic.StoreInt32(&gotTune, 2) // wrong frequency!
				}
				// stream 3 correctly-framed IQ SND frames per owning source
				// (rx_sound.h snd_pkt_iq_t): 20-byte header, flag 0x08,
				// seq LE, smeter BE, GPS fields LE, int16 I/Q payload (BE).
				for seq := byte(0); seq < 3; seq++ {
					frame := []byte{'S', 'N', 'D', 0x08, seq, 0, 0, 0, 4, 244, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
					payload := make([]byte, 512)
					for i := 0; i+3 < len(payload); i += 4 {
						payload[i], payload[i+1], payload[i+2], payload[i+3] = 0x10, 0x00, 0xF0, 0x00
					}
					frame = append(frame, payload...)
					conn.WriteMessage(websocket.BinaryMessage, frame)
				}
			}
		}
	}))
	return srv, websocket.Dialer{HandshakeTimeout: 3 * time.Second}, &gotTune
}

func TestKiwiStage2ObservedDefaultGetsIQ(t *testing.T) {
	srv, d, gotTune := fakeKiwi(t, true)
	defer srv.Close()
	c := candidate{EndpointKey: strings.TrimPrefix(srv.URL, "http://"), URL: srv.URL, Family: "KiwiSDR"}
	var r result
	probeKiwiStage2Dial(c, &r, d)
	if r.Outcome != "iq-samples-decoded" {
		t.Fatalf("outcome=%q err=%s facts=%s", r.Outcome, r.Err, r.Facts)
	}
	if atomic.LoadInt32(gotTune) != 1 {
		t.Fatalf("tune state=%d (2=wrong freq, 0=never tuned)", atomic.LoadInt32(gotTune))
	}
	if !strings.Contains(r.Facts, "init_freq_khz=7020.000") || !strings.Contains(r.Facts, "snd_frames=3") {
		t.Fatalf("facts=%s", r.Facts)
	}
}

func TestKiwiStage2NoDefaultStopsWithoutTune(t *testing.T) {
	srv, d, gotTune := fakeKiwi(t, false)
	defer srv.Close()
	c := candidate{EndpointKey: strings.TrimPrefix(srv.URL, "http://"), URL: srv.URL, Family: "KiwiSDR"}
	var r result
	probeKiwiStage2Dial(c, &r, d)
	if r.Outcome != "no-passive-default-freq" {
		t.Fatalf("outcome=%q facts=%s", r.Outcome, r.Facts)
	}
	if atomic.LoadInt32(gotTune) != 0 {
		t.Fatal("probe tuned without an observed default - forbidden")
	}
}

// fakeOWRXStream: config with active profile, then binary frames after dspcontrol start.
func TestOpenWebRXStage2AudioFrames(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := upgrader.Upgrade(w, req, nil)
		if err != nil { return }
		defer conn.Close()
		conn.ReadMessage() // greeting
		conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"config","value":{"profile_id":"20m","sdr_id":"rtlsdr","center_freq":14175000,"start_freq":14070000,"start_mod":"usb"}}`))
		for {
			mt, data, err := conn.ReadMessage()
			if err != nil { return }
			if mt == websocket.TextMessage && strings.Contains(string(data), "dspcontrol") {
				for i := 0; i < 2; i++ {
					conn.WriteMessage(websocket.BinaryMessage, append([]byte{0x02}, make([]byte, 100)...))
				}
				conn.WriteMessage(websocket.BinaryMessage, append([]byte{0x01}, make([]byte, 50)...))
			}
		}
	}))
	defer srv.Close()
	c := candidate{EndpointKey: strings.TrimPrefix(srv.URL, "http://"), URL: srv.URL, Family: "OpenWebRX"}
	var r result
	probeOpenWebRXStage2Dial(c, &r, websocket.Dialer{HandshakeTimeout: 3 * time.Second})
	if r.Outcome != "audio-frames-received" {
		t.Fatalf("outcome=%q err=%s facts=%s", r.Outcome, r.Err, r.Facts)
	}
	if !strings.Contains(r.Facts, "audio_frames=2") || !strings.Contains(r.Facts, "center_hz=14175000") {
		t.Fatalf("facts=%s", r.Facts)
	}
}

func TestKiwiRetuneBothFreqs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := upgrader.Upgrade(w, req, nil)
		if err != nil { return }
		defer conn.Close()
		conn.WriteMessage(websocket.TextMessage, []byte("MSG audio_rate=12000 load_cfg="+url.QueryEscape(`{"init":{"freq":7020.0}}`)))
		send := func(fill byte, sm uint16) {
			for seq := byte(0); seq < 4; seq++ {
				frame := []byte{'S', 'N', 'D', 0x08, seq, 0, 0, 0, byte(sm >> 8), byte(sm)}
				frame = append(frame, make([]byte, 256, 256)...)
				for i := 10; i < len(frame); i++ { frame[i] = fill }
				conn.WriteMessage(websocket.BinaryMessage, frame)
			}
		}
		for {
			mt, data, err := conn.ReadMessage()
			if err != nil { return }
			if mt != websocket.TextMessage { continue }
			s := string(data)
			if strings.HasPrefix(s, "SET mod=") { send(0xAA, 1000) }
			if strings.HasPrefix(s, "SET freq=") { send(0x55, 1500) }
		}
	}))
	defer srv.Close()
	c := candidate{EndpointKey: strings.TrimPrefix(srv.URL, "http://"), URL: srv.URL, Family: "KiwiSDR"}
	var r result
	probeKiwiRetuneDial(c, &r, websocket.Dialer{HandshakeTimeout: 3 * time.Second})
	if r.Outcome != "retune-accepted-samples-both" {
		t.Fatalf("outcome=%q err=%s facts=%s", r.Outcome, r.Err, r.Facts)
	}
	if !strings.Contains(r.Facts, "payload_differs=true") || !strings.Contains(r.Facts, "f1_khz=7520.000") {
		t.Fatalf("facts=%s", r.Facts)
	}
}

func TestOpenWebRXRetuneBoth(t *testing.T)        { owrxRetuneCase(t, true, "retune-accepted-samples-both") }
func TestOpenWebRXRetuneSilent(t *testing.T)      { owrxRetuneCase(t, false, "changed-but-no-samples") }

func owrxRetuneCase(t *testing.T, keepAudio bool, want string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := upgrader.Upgrade(w, req, nil)
		if err != nil { return }
		defer conn.Close()
		conn.ReadMessage()
		conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"config","value":{"profile_id":"20m","center_freq":14175000,"start_freq":14070000,"samp_rate":500000}}`))
		audio := func() {
			for i := 0; i < 3; i++ { conn.WriteMessage(websocket.BinaryMessage, append([]byte{0x02}, make([]byte, 60)...)) }
		}
		seenTune := false
		for {
			mt, data, err := conn.ReadMessage()
			if err != nil { return }
			if mt != websocket.TextMessage { continue }
			s := string(data)
			if strings.Contains(s, "dspcontrol") && strings.Contains(s, "start") {
				// synchronous: gorilla conns allow ONE concurrent writer
				if !seenTune || keepAudio { audio() }
			}
			if strings.Contains(s, "frequency") { seenTune = true }
		}
	}))
	defer srv.Close()
	c := candidate{EndpointKey: strings.TrimPrefix(srv.URL, "http://"), URL: srv.URL, Family: "OpenWebRX"}
	var r result
	probeOpenWebRXRetuneDial(c, &r, websocket.Dialer{HandshakeTimeout: 3 * time.Second})
	if r.Outcome != want {
		t.Fatalf("outcome=%q want %q err=%s facts=%s", r.Outcome, want, r.Err, r.Facts)
	}
}

// --- UberSDR leg pins, all offline ---

type uberFake struct {
	t          *testing.T
	status     int
	body       string
	posts      int
	gotPW      string
	gotSession string
	gotUA      string
}

func (f *uberFake) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Path != "/connection" || req.Method != http.MethodPost {
		f.t.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
		return &http.Response{StatusCode: 404, Body: http.NoBody, Header: make(http.Header), Request: req}, nil
	}
	f.posts++
	raw, _ := io.ReadAll(req.Body)
	var b map[string]string
	_ = json.Unmarshal(raw, &b)
	f.gotPW = b["password"]
	f.gotSession = b["user_session_id"]
	f.gotUA = req.Header.Get("User-Agent")
	return &http.Response{
		StatusCode: f.status,
		Body:       io.NopCloser(strings.NewReader(f.body)),
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

const uberTestSID = "11111111-2222-4333-8444-555555555555"

func TestUberCheckWideAllowed(t *testing.T) {
	f := &uberFake{t: t, status: 200, body: `{"allowed":true,"allowed_iq_modes":["iq96","iq48","audio"]}`}
	r := result{}
	wide, ok := probeUberCheck(context.Background(), candidate{URL: "http://uber.example", EndpointKey: "uber.example"}, &r, f, uberTestSID)
	if !ok || len(wide) != 2 {
		t.Fatalf("wide=%v ok=%v r=%+v", wide, ok, r)
	}
	if r.Outcome != "" {
		t.Fatalf("success leaves the verdict to the WS stage, got %q", r.Outcome)
	}
	if f.posts != 1 {
		t.Fatalf("posts=%d, want exactly 1 (no retries, one contact)", f.posts)
	}
	if f.gotPW != "" {
		t.Fatal("password pin violated: must be empty")
	}
	if f.gotSession != uberTestSID {
		t.Fatal("session id must be the shared UUID")
	}
	if !strings.Contains(f.gotUA, "sdrstream-uber-precheck") {
		t.Fatalf("honest UA missing: %q", f.gotUA)
	}
}

func TestUberCheckRateLimitedStops(t *testing.T) {
	f := &uberFake{t: t, status: 429, body: `{"allowed":false,"reason":"too many sessions"}`}
	r := result{}
	if _, ok := probeUberCheck(context.Background(), candidate{URL: "http://uber.example"}, &r, f, uberTestSID); ok {
		t.Fatal("429 must not proceed to the WS stage")
	}
	if r.Outcome != "answered-rate-limited" || f.posts != 1 {
		t.Fatalf("got %+v posts=%d", r, f.posts)
	}
}

func TestUberCheckNotAllowedAndNoWide(t *testing.T) {
	f := &uberFake{t: t, status: 200, body: `{"allowed":false,"reason":"password required"}`}
	r := result{}
	probeUberCheck(context.Background(), candidate{URL: "http://uber.example"}, &r, f, uberTestSID)
	if r.Outcome != "answered-not-allowed" {
		t.Fatalf("got %+v", r)
	}
	f2 := &uberFake{t: t, status: 200, body: `{"allowed":true,"allowed_iq_modes":["audio","iq"]}`}
	r2 := result{}
	probeUberCheck(context.Background(), candidate{URL: "http://uber.example"}, &r2, f2, uberTestSID)
	if r2.Outcome != "answered-no-public-wide-mode" {
		t.Fatalf("got %+v", r2)
	}
}

// Full leg: precheck via fake transport, WS via a real local upgrade.
// Asserts the pins upstream cares about: no frequency parameter (server
// default), smallest wide mode, shared UUID session, honest UA.
func TestUberFullLeg(t *testing.T) {
	f := &uberFake{t: t, status: 200, body: `{"allowed":true,"allowed_iq_modes":["iq96","iq48"]}`}
	up := websocket.Upgrader{}
	var gotQuery url.Values
	var gotWSUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotQuery = req.URL.Query()
		gotWSUA = req.Header.Get("User-Agent")
		conn, err := up.Upgrade(w, req, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()
		enc := v4enc.New()
		samples := make([]int16, 2*238) // one small IQ packet
		for i := range samples {
			samples[i] = int16(i * 97)
		}
		pkt, err := enc.Encode(samples, 48000, 2, 0)
		if err != nil {
			t.Errorf("encode: %v", err)
			return
		}
		_ = conn.WriteMessage(websocket.BinaryMessage, pkt)
		_ = conn.WriteMessage(websocket.BinaryMessage, pkt)
	}))
	defer srv.Close()
	r := result{}
	probeUber(candidate{URL: srv.URL, EndpointKey: "uber.example"}, &r, f, &websocket.Dialer{HandshakeTimeout: 3 * time.Second})
	if r.Outcome != "iq-samples-decoded" {
		t.Fatalf("got %+v", r)
	}
	if !strings.Contains(r.MsgTypes, "samples=952") || !strings.Contains(r.MsgTypes, "rate=48000") || !strings.Contains(r.MsgTypes, "channels=2") {
		t.Fatalf("decoded IQ facts missing: %q", r.MsgTypes)
	}
	if _, has := gotQuery["frequency"]; has {
		t.Fatal("frequency parameter must be omitted - the server default applies, nothing invented")
	}
	if gotQuery.Get("mode") != "iq48" {
		t.Fatalf("mode=%q, want iq48 (smallest allowed wide mode)", gotQuery.Get("mode"))
	}
	if gotQuery.Get("user_session_id") == "" || gotQuery.Get("user_session_id") == "rxprobe-" {
		t.Fatal("session id must be the shared UUID")
	}
	if !strings.Contains(gotWSUA, "sdrstream-uber-precheck") {
		t.Fatalf("WS UA %q must match the precheck UA", gotWSUA)
	}
	if !strings.Contains(r.Writes, "server default 14074000") {
		t.Fatal("Writes must record the server-default frequency provenance")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// --- Web-888 (RaspSDR fork) standalone parser + leg, all offline ---

// buildWeb888Pkt assembles a packet exactly as the fork's snd_pkt_iq_t lays
// it out (rx_sound.h:56-70 @ 6445a007): 20-byte header, header multi-byte
// fields little-endian, payload int16 BE unless flag 0x80.
func buildWeb888Pkt(flags byte, seq uint32, samples []int16) []byte {
	pkt := make([]byte, web888IQHeaderLen+2*len(samples))
	copy(pkt, "SND")
	pkt[3] = flags
	binary.LittleEndian.PutUint32(pkt[4:8], seq)
	pkt[8], pkt[9] = 0x11, 0x22 // smeter
	pkt[10] = 3                 // last_gps_solution
	pkt[11] = 0                 // dummy
	binary.LittleEndian.PutUint32(pkt[12:16], 604800)
	binary.LittleEndian.PutUint32(pkt[16:20], 123456789)
	payload := pkt[web888IQHeaderLen:]
	for i, v := range samples {
		if flags&web888FlagLittleEndian != 0 {
			binary.LittleEndian.PutUint16(payload[2*i:], uint16(v))
		} else {
			binary.BigEndian.PutUint16(payload[2*i:], uint16(v))
		}
	}
	return pkt
}

func TestWeb888ParserBigEndianDefault(t *testing.T) {
	in := []int16{0, 1, -1, 32767, -32768, 12345, -23456, 42}
	pkt := buildWeb888Pkt(web888FlagModeIQ, 0xdeadbeef, in)
	f, err := parseWeb888IQFrame(pkt)
	if err != nil {
		t.Fatal(err)
	}
	if f.Seq != 0xdeadbeef || f.GpsSec != 604800 || f.GpsNSec != 123456789 {
		t.Fatalf("header fields: %+v", f)
	}
	if len(f.Samples) != len(in) {
		t.Fatalf("samples %d != %d", len(f.Samples), len(in))
	}
	for i := range in {
		if f.Samples[i] != in[i] {
			t.Fatalf("sample %d: got %d want %d", i, f.Samples[i], in[i])
		}
	}
}

func TestWeb888ParserLittleEndianFlag(t *testing.T) {
	in := []int16{-300, 700, 1024, -1024}
	pkt := buildWeb888Pkt(web888FlagModeIQ|web888FlagLittleEndian, 1, in)
	f, err := parseWeb888IQFrame(pkt)
	if err != nil {
		t.Fatal(err)
	}
	for i := range in {
		if f.Samples[i] != in[i] {
			t.Fatalf("LE sample %d: got %d want %d", i, f.Samples[i], in[i])
		}
	}
}

func TestWeb888ParserRejects(t *testing.T) {
	if _, err := parseWeb888IQFrame(buildWeb888Pkt(0x10, 1, []int16{1, 2})); err != errWeb888NotIQ {
		t.Fatalf("non-IQ flag must be rejected: %v", err)
	}
	if _, err := parseWeb888IQFrame([]byte("SND\x08short")); err == nil {
		t.Fatal("short packet must be rejected")
	}
	bad := buildWeb888Pkt(web888FlagModeIQ, 1, []int16{1})
	if _, err := parseWeb888IQFrame(bad[:len(bad)-1]); err == nil {
		t.Fatal("odd payload must be rejected")
	}
	if _, err := parseWeb888IQFrame(buildWeb888Pkt(web888FlagModeIQ, 1, nil)); err != nil {
		t.Fatalf("empty payload is a valid zero-sample IQ packet: %v", err)
	}
}

// web888Fake serves /status (fork-shaped: NO ext_api field) and a WS SND
// endpoint speaking the fork wire layout.
type web888Fake struct {
	t            *testing.T
	statusBody   string
	mu           sync.Mutex
	sentMsgs     []string
	statusHits   int
	sawStatus    bool
	initFreqKHz  float64
	audioRate    float64
	tooBusy      bool
	payloadFlags byte
}

func (f *web888Fake) handler() http.Handler {
	up := websocket.Upgrader{}
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/status" {
			f.statusHits++
			_, _ = w.Write([]byte(f.statusBody))
			return
		}
		conn, err := up.Upgrade(w, req, nil)
		if err != nil {
			f.t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()
		if f.tooBusy {
			_ = conn.WriteMessage(websocket.BinaryMessage, []byte("MSG too_busy=3"))
			_, _, _ = conn.ReadMessage() // let the client hang up first
			return
		}
		cfg := url.QueryEscape(fmt.Sprintf(`{"init":{"freq":%g,"mode":"am"}}`, f.initFreqKHz))
		_ = conn.WriteMessage(websocket.BinaryMessage, []byte("MSG audio_rate="+strconv.Itoa(int(f.audioRate))+" freq_offset=0"))
		_ = conn.WriteMessage(websocket.BinaryMessage, []byte("MSG load_cfg="+cfg))
		for {
			mt, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if mt == websocket.TextMessage {
				f.mu.Lock()
				f.sentMsgs = append(f.sentMsgs, string(data))
				f.mu.Unlock()
				if strings.HasPrefix(string(data), "SET mod=iq") {
					pkt := buildWeb888Pkt(f.payloadFlags, 7, []int16{100, -100, 200, -200, 300, -300})
					for i := 0; i < 3; i++ {
						if err := conn.WriteMessage(websocket.BinaryMessage, pkt); err != nil {
							return
						}
					}
				}
			}
		}
	})
}

func web888StatusRT(f *web888Fake) http.RoundTripper {
	return roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/status" {
			return &http.Response{StatusCode: 404, Body: http.NoBody, Header: make(http.Header), Request: req}, nil
		}
		f.sawStatus = true
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(f.statusBody)), Header: make(http.Header), Request: req}, nil
	})
}

func TestWeb888FullLegDecodesIQ(t *testing.T) {
	f := &web888Fake{t: t, statusBody: "status=active\nchannels=4\n", initFreqKHz: 7040.0, audioRate: 12000, payloadFlags: web888FlagModeIQ}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	r := result{}
	probeWeb888(candidate{EndpointKey: host}, &r, web888StatusRT(f), &websocket.Dialer{HandshakeTimeout: 3 * time.Second})
	if r.Outcome != "iq-samples-decoded" {
		t.Fatalf("got %+v", r)
	}
	if !strings.Contains(r.Writes, "no ext_api field - expected on RaspSDR fork") {
		t.Fatalf("missing-field provenance not recorded: %q", r.Writes)
	}
	sentMod := ""
	f.mu.Lock()
	for _, m := range f.sentMsgs {
		if strings.HasPrefix(m, "SET mod=iq") {
			sentMod = m
		}
	}
	f.mu.Unlock()
	if !strings.Contains(sentMod, "freq=7040.000") {
		t.Fatalf("mod=iq must target the observed default only, got %q", sentMod)
	}
	if !strings.Contains(r.Facts, "parsed_iq_int16=18") {
		t.Fatalf("expected 3 packets x 6 int16 = 18 decoded: %q", r.Facts)
	}
}

func TestWeb888TooBusyStopsNoTune(t *testing.T) {
	f := &web888Fake{t: t, statusBody: "status=active\n", tooBusy: true}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	r := result{}
	probeWeb888(candidate{EndpointKey: host}, &r, web888StatusRT(f), &websocket.Dialer{HandshakeTimeout: 3 * time.Second})
	if r.Outcome != "refused-busy" {
		t.Fatalf("got %+v", r)
	}
	f.mu.Lock()
	for _, m := range f.sentMsgs {
		if strings.HasPrefix(m, "SET mod=") {
			t.Fatalf("no tuning after too_busy, sent %q", m)
		}
	}
	f.mu.Unlock()
}

func TestWeb888ExplicitExtAPIZeroSkips(t *testing.T) {
	f := &web888Fake{t: t, statusBody: "status=active\next_api=0\n", initFreqKHz: 7040}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	r := result{}
	probeWeb888(candidate{EndpointKey: host}, &r, web888StatusRT(f), &websocket.Dialer{HandshakeTimeout: 3 * time.Second})
	if r.Outcome != "skipped-ext-api-zero" {
		t.Fatalf("a present ext_api=0 is still a hard skip: %+v", r)
	}
}
