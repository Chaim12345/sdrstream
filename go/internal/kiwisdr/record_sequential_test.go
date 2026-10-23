package kiwisdr

import (
	"context"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// slowFrameServer streams one uncompressed SND frame every frameGap,
// answering tunes like retuneServer. The wide gap lets a Record read
// deadline fall inside a frame gap, which is how the real Kiwi wedged.
func slowFrameServer(t *testing.T, frameGap time.Duration) *httptest.Server {
	t.Helper()
	up := websocket.Upgrader{}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		go func() {
			for {
				if _, _, err := conn.ReadMessage(); err != nil {
					return
				}
			}
		}()
		_ = conn.WriteMessage(websocket.TextMessage, []byte("MSG sample_rate=12000"))
		for {
			frame := make([]byte, 10+200)
			copy(frame[:3], "SND")
			for i := 0; i < 100; i++ {
				binary.BigEndian.PutUint16(frame[10+i*2:], 4000)
			}
			if err := conn.WriteMessage(websocket.BinaryMessage, frame); err != nil {
				return
			}
			time.Sleep(frameGap)
		}
	}))
}

// A gorilla read timeout poisons a websocket connection for every later
// read. Record must therefore end on its duration timer, never on the read
// deadline: two sequential Records on one connection must both get audio.
// Regression test for the readTimeout clamp at the end of Record.
func TestRecordSequentialRecordsBothGetAudio(t *testing.T) {
	srv := slowFrameServer(t, 100*time.Millisecond)
	defer srv.Close()
	c := connectedClient(t, srv)
	ctx := context.Background()
	if err := c.Tune(ctx, 7_000_000, "usb"); err != nil {
		t.Fatalf("tune: %v", err)
	}
	// 305 ms ends the record inside the 100 ms frame gap that starts at
	// 300 ms, so the last read deadline lands mid-gap (the real-Kiwi case).
	s1, _, err := c.Record(ctx, 305*time.Millisecond)
	if err != nil {
		t.Fatalf("record 1: %v", err)
	}
	if len(s1) == 0 {
		t.Fatal("record 1: no samples")
	}
	s2, _, err := c.Record(ctx, 300*time.Millisecond)
	if err != nil {
		t.Fatalf("record 2 (connection wedged by first record's deadline): %v", err)
	}
	if len(s2) == 0 {
		t.Fatal("record 2: no samples; first record's read timeout poisoned the connection")
	}
}
