package decode

import (
	"context"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakeKiwi speaks just enough of the KiwiSDR websocket protocol for
// session tests: it accepts the upgrade, ignores SET lines, and streams
// uncompressed SND frames of a quiet tone.
type fakeKiwi struct {
	connections *int32
	tunes       *int32
}

func (f *fakeKiwi) serve(t *testing.T) *httptest.Server {
	t.Helper()
	up := websocket.Upgrader{}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		atomic.AddInt32(f.connections, 1)
		defer conn.Close()
		_ = conn.WriteMessage(websocket.TextMessage, []byte("MSG sample_rate=12000"))
		go func() {
			for {
				kind, _, err := conn.ReadMessage()
				if err != nil || kind != websocket.TextMessage {
					return
				}
				atomic.AddInt32(f.tunes, 1) // every SET line counts; good enough
			}
		}()
		frame := make([]byte, 3+1+4+2+2048)
		copy(frame[:3], "SND")
		for i := 0; i < 1024; i++ {
			binary.BigEndian.PutUint16(frame[8+i*2:], uint16(300))
		}
		for {
			if err := conn.WriteMessage(websocket.BinaryMessage, frame); err != nil {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}))
}

func TestRunHFSessionCapsAndSingleConnection(t *testing.T) {
	var conns, tunes int32
	fake := &fakeKiwi{connections: &conns, tunes: &tunes}
	srv := fake.serve(t)
	defer srv.Close()
	server := strings.TrimPrefix(srv.URL, "http://")
	targets := make([]HFTarget, 25)
	for i := range targets {
		targets[i] = HFTarget{
			Name: "fake", Kind: "rtty",
			DialKHz: 7000 + float64(i), Mode: "usb",
			Baud: 50, MarkHz: 1675, SpaceHz: 2125,
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	results, remaining, err := RunHFSession(ctx, server, targets, SessionOptions{
		Listen:       300 * time.Millisecond,
		MaxTargets:   3,
		AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Errorf("results = %d, want 3", len(results))
	}
	if len(remaining) != 22 {
		t.Errorf("remaining = %d, want 22", len(remaining))
	}
	if got := atomic.LoadInt32(&conns); got != 1 {
		t.Errorf("connections = %d, want exactly 1", got)
	}
	// Gate must have rejected the quiet-tone audio as no decode.
	for _, r := range results {
		if r.Gate.Live {
			t.Errorf("tone audio passed the gate: %s", r.Gate.Reason)
		}
	}
}

func TestRunHFSessionSessionLimit(t *testing.T) {
	var conns, tunes int32
	fake := &fakeKiwi{connections: &conns, tunes: &tunes}
	srv := fake.serve(t)
	defer srv.Close()
	server := strings.TrimPrefix(srv.URL, "http://")
	targets := make([]HFTarget, 10)
	for i := range targets {
		targets[i] = HFTarget{Name: "fake", Kind: "rtty", DialKHz: 7000, Mode: "usb", Baud: 50, MarkHz: 1675, SpaceHz: 2125}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	results, remaining, err := RunHFSession(ctx, server, targets, SessionOptions{
		Listen:       500 * time.Millisecond,
		MaxTargets:   20,
		MaxSession:   1200 * time.Millisecond,
		AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) > 3 {
		t.Errorf("session limit ignored: %d results", len(results))
	}
	if len(results)+len(remaining) != 10 {
		t.Errorf("results %d + remaining %d != 10", len(results), len(remaining))
	}
}

func TestRunHFSessionSkipsInactive(t *testing.T) {
	var conns, tunes int32
	fake := &fakeKiwi{connections: &conns, tunes: &tunes}
	srv := fake.serve(t)
	defer srv.Close()
	server := strings.TrimPrefix(srv.URL, "http://")
	targets := []HFTarget{
		{Name: "on-air rtty", Kind: "rtty", DialKHz: 4581.1, Mode: "usb", Baud: 50, MarkHz: 1675, SpaceHz: 2125},
		{Name: "off-air fax", Kind: "wefax", DialKHz: 3853.1, Mode: "usb", LinesPerMin: 120, IOC: 576},
	}
	// 03:00 UTC is outside every DWD fax window, so only rtty runs.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	results, remaining, err := RunHFSession(ctx, server, targets, SessionOptions{
		Listen:       300 * time.Millisecond,
		MaxTargets:   20,
		SkipInactive: true,
		AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	fax := targets[1]
	if fax.ActiveNow(time.Now()) {
		t.Skip("test only meaningful outside DWD fax windows (19:40-20:59 UTC)")
	}
	if len(results) != 1 || results[0].Target.Name != "on-air rtty" {
		t.Errorf("SkipInactive did not filter: %+v", results)
	}
	if len(remaining) != 0 {
		t.Errorf("remaining = %d, want 0", len(remaining))
	}
}
