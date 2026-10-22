package kiwisdr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// dropServer accepts the upgrade then closes the connection immediately,
// simulating a flapping receiver.
func dropServer(t *testing.T, conns *int32) *httptest.Server {
	t.Helper()
	up := websocket.Upgrader{}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		atomic.AddInt32(conns, 1)
		// Stay up long enough for the client's handshake writes to land,
		// then drop with an abnormal server-side close: a flapping receiver.
		time.Sleep(50 * time.Millisecond)
		_ = conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "boom"))
		// Give the client a moment to read the close frame.
		_, _, _ = conn.ReadMessage()
		conn.Close()
	}))
}

func TestRecordCapsReconnects(t *testing.T) {
	var conns int32
	srv := dropServer(t, &conns)
	defer srv.Close()
	opts := DefaultOptions()
	opts.AllowPrivate = true
	opts.OpenTimeout = time.Second
	opts.IdleTimeout = 300 * time.Millisecond
	c, err := New(strings.TrimPrefix(srv.URL, "http://"), opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, _, err = c.Record(context.Background(), 30*time.Second)
	if err == nil {
		t.Fatal("a server that drops every session must surface an error, not retry forever")
	}
	if got := atomic.LoadInt32(&conns); got != 1+maxRecordReconnects {
		t.Fatalf("connections = %d, want %d (initial + capped reconnects)", got, 1+maxRecordReconnects)
	}
}

func TestRecordHonoursContextDuringSilentRead(t *testing.T) {
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		// Say nothing; Record must still notice ctx ending.
		select {}
	}))
	defer srv.Close()
	opts := DefaultOptions()
	opts.AllowPrivate = true
	opts.OpenTimeout = time.Second
	opts.IdleTimeout = 30 * time.Second // deliberately long: ctx must win
	c, err := New(strings.TrimPrefix(srv.URL, "http://"), opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, err = c.Record(ctx, 30*time.Second)
	if err == nil {
		t.Fatal("ctx cancellation must end Record")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("Record ignored ctx for %s", elapsed)
	}
}

func TestRecordHonoursDurationDuringSilentRead(t *testing.T) {
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
	opts := DefaultOptions()
	opts.AllowPrivate = true
	opts.OpenTimeout = time.Second
	opts.IdleTimeout = 30 * time.Second
	c, err := New(strings.TrimPrefix(srv.URL, "http://"), opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	start := time.Now()
	if _, _, err := c.Record(context.Background(), 300*time.Millisecond); err != nil {
		t.Fatalf("duration expiry is a clean end, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("Record ignored its duration for %s", elapsed)
	}
}
