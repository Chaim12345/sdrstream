package kiwisdr

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

const (
	oldTone = 1000 // audio of the previous frequency
	newTone = 5000 // audio after the tune
)

// retuneServer streams oldTone frames until it reads a "SET mod=" tune
// line, keeps sending oldTone for inFlight longer (audio already queued in
// the stream), then switches to newTone. Frames are ~10 ms apart.
func retuneServer(t *testing.T, inFlight time.Duration) *httptest.Server {
	t.Helper()
	up := websocket.Upgrader{}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		var switchAt atomic.Int64 // unix nanos; 0 = not tuned yet
		go func() {
			for {
				kind, data, err := conn.ReadMessage()
				if err != nil {
					return
				}
				if kind == websocket.TextMessage && strings.HasPrefix(string(data), "SET mod=") {
					switchAt.Store(time.Now().Add(inFlight).UnixNano())
				}
			}
		}()
		_ = conn.WriteMessage(websocket.TextMessage, []byte("MSG sample_rate=12000"))
		for {
			v := uint16(oldTone)
			if at := switchAt.Load(); at != 0 && time.Now().UnixNano() >= at {
				v = newTone
			}
			frame := make([]byte, 10+200)
			copy(frame[:3], "SND")
			for i := 0; i < 100; i++ {
				binary.BigEndian.PutUint16(frame[10+i*2:], v)
			}
			if err := conn.WriteMessage(websocket.BinaryMessage, frame); err != nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}))
}

func connectedClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	opts := DefaultOptions()
	opts.AllowPrivate = true
	opts.OpenTimeout = time.Second
	opts.IdleTimeout = time.Second
	c, err := New(strings.TrimPrefix(srv.URL, "http://"), opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestRecordAfterTuneDropsInFlightAudioButKeepsFullDuration(t *testing.T) {
	old := tuneSettle
	tuneSettle = 400 * time.Millisecond
	defer func() { tuneSettle = old }()
	srv := retuneServer(t, 150*time.Millisecond)
	defer srv.Close()
	c := connectedClient(t, srv)
	time.Sleep(100 * time.Millisecond) // stream is running on the old frequency
	if err := c.Tune(context.Background(), 7_000_000, "usb"); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	samples, _, err := c.Record(context.Background(), 300*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	for i, s := range samples {
		if s == oldTone {
			t.Fatalf("sample %d is old-frequency audio (%d): pre-tune audio leaked into the capture", i, s)
		}
	}
	// 300 ms at ~10k samples/s from the fake is about 3000; the discard
	// must not have eaten into it.
	if len(samples) < 2000 {
		t.Fatalf("captured %d samples, want about 3000 (full duration after the settle)", len(samples))
	}
	if elapsed < 600*time.Millisecond {
		t.Fatalf("Record returned after %v; settle (400ms) + duration (300ms) not honoured", elapsed)
	}
}

func TestRecordWithoutTuneDoesNotDiscard(t *testing.T) {
	srv := retuneServer(t, 0)
	defer srv.Close()
	c := connectedClient(t, srv)
	start := time.Now()
	samples, _, err := c.Record(context.Background(), 300*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 450*time.Millisecond {
		t.Fatalf("Record without a prior tune took %v, expected about 300ms", time.Since(start))
	}
	if len(samples) < 2000 {
		t.Fatalf("captured %d samples, want about 3000", len(samples))
	}
}

func TestSettleHonoursContextCancel(t *testing.T) {
	old := tuneSettle
	tuneSettle = 5 * time.Second
	defer func() { tuneSettle = old }()
	srv := retuneServer(t, time.Hour)
	defer srv.Close()
	c := connectedClient(t, srv)
	if err := c.Tune(context.Background(), 7_000_000, "usb"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, err := c.Record(ctx, time.Second)
	if err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("settle ignored ctx: err=%v after %v", err, time.Since(start))
	}
}
