package websdr

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// testOptions dials loopback test servers, which the SSRF guard refuses
// by default.
func testOptions() Options {
	o := DefaultOptions()
	o.AllowPrivate = true
	o.OpenTimeout = time.Second
	o.IdleTimeout = 300 * time.Millisecond
	return o
}

// fakeWebSDR speaks just enough of the protocol: it upgrades, records the
// retune frames it receives, and streams a fixed vocoder-free pattern the
// client treats as binary frames.
type fakeWebSDR struct {
	mu       sync.Mutex
	frames   [][]byte // text frames received (tune requests)
	onBinary func(conn *websocket.Conn)
	drop     bool // close with an abnormal close frame after connecting
}

func (f *fakeWebSDR) server(t *testing.T) *httptest.Server {
	t.Helper()
	up := websocket.Upgrader{}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, StreamPath) {
			http.NotFound(w, r)
			return
		}
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		if f.drop {
			_ = conn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "boom"))
			return
		}
		done := make(chan struct{})
		go func() {
			for {
				select {
				case <-done:
					return
				default:
				}
				// Vocoder frames: 0x87 timestamp control byte plus data.
				if err := conn.WriteMessage(websocket.BinaryMessage, []byte{0x87, 1, 2, 3, 4}); err != nil {
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
		}()
		defer close(done)
		for {
			kind, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if kind == websocket.TextMessage {
				f.mu.Lock()
				f.frames = append(f.frames, append([]byte(nil), data...))
				f.mu.Unlock()
			}
		}
	}))
}

func (f *fakeWebSDR) textFrames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.frames))
	for i, b := range f.frames {
		out[i] = string(b)
	}
	return out
}

func TestTuneSendsParamFrame(t *testing.T) {
	fake := &fakeWebSDR{}
	srv := fake.server(t)
	defer srv.Close()
	c, err := New(srv.URL, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Tune(context.Background(), 7074.0, "usb", nil); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		frames := fake.textFrames()
		if len(frames) == 1 && strings.HasPrefix(frames[0], "GET /~~param?") && strings.Contains(frames[0], "f=7074.000") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("tune frames: %v", fake.textFrames())
}

func TestRecordReturnsReaderErrorNotSilence(t *testing.T) {
	fake := &fakeWebSDR{drop: true}
	srv := fake.server(t)
	defer srv.Close()
	opts := testOptions()
	opts.Reconnect = false
	c, err := New(srv.URL, opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	samples, _, err := c.Record(context.Background(), time.Second)
	if err == nil {
		t.Fatal("a dead stream must return the reader error, not clean silence")
	}
	if len(samples) != 0 {
		t.Fatalf("got %d samples from a dead stream", len(samples))
	}
}

func TestRecordSilenceWithoutErrorStaysClean(t *testing.T) {
	// A server that sends nothing but stays connected: Record must report
	// an empty, error-free capture (a quiet receiver, not a failure).
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		select {}
	}))
	defer srv.Close()
	c, err := New(srv.URL, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, _, err = c.Record(context.Background(), 200*time.Millisecond)
	if err != nil {
		t.Fatalf("quiet but alive receiver must not error: %v", err)
	}
}

func TestCloseClosesSocket(t *testing.T) {
	var closed atomic.Bool
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				closed.Store(true)
				return
			}
		}
	}))
	defer srv.Close()
	c, err := New(srv.URL, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.Close()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if closed.Load() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("server never saw the socket close")
}

func TestRetuneAfterReconnectUsesDataFrame(t *testing.T) {
	var conns atomic.Int32
	var sawTune atomic.Bool
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		if conns.Add(1) == 1 {
			// First connection dies abnormally.
			_ = conn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "boom"))
			return
		}
		// Second connection: a retune must arrive as a text data frame.
		// ReadMessage rejects control frames here, so receiving it at all
		// proves WriteMessage was used.
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		kind, data, err := conn.ReadMessage()
		if err == nil && kind == websocket.TextMessage && strings.HasPrefix(string(data), "GET /~~param?") {
			sawTune.Store(true)
		}
	}))
	defer srv.Close()
	opts := testOptions()
	opts.Reconnect = true
	opts.ReconnectInitial = 10 * time.Millisecond
	opts.ReconnectMax = 50 * time.Millisecond
	c, err := New(srv.URL, opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Tune(context.Background(), 14074.0, "usb", nil); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if conns.Load() >= 2 && sawTune.Load() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("conns=%d sawTune=%v", conns.Load(), sawTune.Load())
}

func TestDrainAudioDropsBufferedAudio(t *testing.T) {
	c, err := New("http://websdr.invalid/", testOptions())
	if err != nil {
		t.Fatal(err)
	}
	c.events <- Event{Type: "audio", Value: Chunk{Samples: []int16{1, 2, 3}}}
	c.events <- Event{Type: "text", Value: "hello"}
	c.events <- Event{Type: "audio", Value: Chunk{Samples: []int16{4}}}
	c.DrainAudio()
	event := <-c.events
	if event.Type != "text" {
		t.Fatalf("non-audio event lost: %v", event.Type)
	}
	select {
	case extra := <-c.events:
		t.Fatalf("audio survived the drain: %v", extra.Type)
	default:
	}
}

func TestDialRefusesPrivateHost(t *testing.T) {
	// The guarded default must reject loopback before any network traffic.
	c, err := New("http://127.0.0.1:80/", DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	err = c.Connect(context.Background())
	if err == nil || !strings.Contains(err.Error(), "non-public") {
		t.Fatalf("want SSRF refusal, got %v", err)
	}
	var closedCheck = errors.Is(err, context.Canceled)
	if closedCheck {
		t.Fatal("must fail before dialing, not via ctx")
	}
}
