package iqsrc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// loopback-only fake KiwiSDR SND stream; no external contact.
func fakeKiwiIQServer(t *testing.T, frames [][]byte) *httptest.Server {
	up := websocket.Upgrader{}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/SND") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.WriteMessage(websocket.TextMessage, []byte("MSG sample_rate=12000"))
		for _, f := range frames {
			if c.WriteMessage(websocket.BinaryMessage, f) != nil {
				return
			}
		}
		// Hold the socket like an idle server.
		_ = c.WriteMessage(websocket.TextMessage, []byte("MSG keepalive"))
		time.Sleep(300 * time.Millisecond)
	}))
}

// kiwiIQFrame builds a spec 20-byte-header IQ frame (rx_sound.h snd_pkt_iq_t).
func kiwiIQFrame(seq byte, iq [][2]int16) []byte {
	f := []byte{'S', 'N', 'D', 0x08, seq, 0, 0, 0, 4, 244, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	for _, p := range iq {
		f = append(f, byte(p[0]>>8), byte(p[0]), byte(p[1]>>8), byte(p[1]))
	}
	return f
}

func TestDialKiwiIQOffline(t *testing.T) {
	frames := [][]byte{
		kiwiIQFrame(0, [][2]int16{{4096, -4096}, {100, 200}}),
		kiwiIQFrame(1, [][2]int16{{300, 400}}),
	}
	ts := fakeKiwiIQServer(t, frames)
	defer ts.Close()
	host := strings.TrimPrefix(ts.URL, "http://")

	// Public-only dial guard on by default: loopback refused before connect.
	if _, err := DialKiwiIQ(context.Background(), KiwiIQConfig{Server: host, Frequency: 7_000_000}); err == nil {
		t.Fatal("loopback accepted without AllowPrivate")
	}

	c, err := DialKiwiIQ(context.Background(), KiwiIQConfig{Server: host, Frequency: 7_000_000, AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.SampleRate() != 12000 {
		t.Fatalf("rate %d, want server-announced 12000", c.SampleRate())
	}
	buf := make([]complex64, 2)
	var got []complex64
	deadline := time.Now().Add(3 * time.Second)
	for len(got) < 3 && time.Now().Before(deadline) {
		n, err := c.ReadSamples(buf)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, buf[:n]...)
	}
	if len(got) != 3 {
		t.Fatalf("total %d, want 3 decoded IQ samples", len(got))
	}
	// First sample: I=4096/32768, Q=-4096/32768 (big-endian default).
	if s0 := got[0]; real(s0) != 0.125 || imag(s0) != -0.125 {
		t.Fatalf("sample0 %v", s0)
	}
	if c.BadFrames() != 0 {
		t.Fatalf("bad frames %d", c.BadFrames())
	}
}

func TestDialKiwiIQBadFrameCounted(t *testing.T) {
	// Flags 0x00 = real-audio frame: rejected by the IQ parser, counted.
	bad := []byte{'S', 'N', 'D', 0x00, 0, 0, 0, 0, 4, 244, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 2, 3, 4}
	ts := fakeKiwiIQServer(t, [][]byte{bad, kiwiIQFrame(1, [][2]int16{{1, 1}})})
	defer ts.Close()
	host := strings.TrimPrefix(ts.URL, "http://")
	c, err := DialKiwiIQ(context.Background(), KiwiIQConfig{Server: host, Frequency: 7_000_000, AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// Dial returns on the sample-rate announcement; the bad frame is met
	// in the read loop.
	buf := make([]complex64, 4)
	if _, err := c.ReadSamples(buf); err != nil {
		t.Fatal(err)
	}
	if c.BadFrames() != 1 {
		t.Fatalf("bad %d, want 1", c.BadFrames())
	}
}
