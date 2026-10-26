package iqsrc

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// Loopback-only fake Web-888 SND stream; no external contact. It announces
// load_cfg with init.freq and a float sample_rate, mirroring the fork's
// "MSG audio_init=0 audio_rate=12000 sample_rate=12000.000000".
func fakeWeb888Server(t *testing.T, initKHz, offsetKHz float64, frames [][]byte) *httptest.Server {
	t.Helper()
	up := websocket.Upgrader{}
	cfg := url.QueryEscape(`{"init":{"freq":14074.0,"mode":"usb"}}`)
	if initKHz > 0 {
		cfg = url.QueryEscape(`{"init":{"freq":` + strings.TrimRight(strings.TrimRight(floatStr(initKHz), "0"), ".") + `,"mode":"usb"}}`)
	}
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
		if initKHz > 0 {
			_ = c.WriteMessage(websocket.TextMessage, []byte("MSG load_cfg="+cfg+" freq_offset="+floatStr(offsetKHz)))
		}
		_ = c.WriteMessage(websocket.TextMessage, []byte("MSG audio_init=0 audio_rate=12000 sample_rate=12000.000000"))
		for _, f := range frames {
			if c.WriteMessage(websocket.BinaryMessage, f) != nil {
				return
			}
		}
		_ = c.WriteMessage(websocket.TextMessage, []byte("MSG keepalive"))
		hold := 300 * time.Millisecond
		if initKHz == 0 {
			hold = 6 * time.Second // outlast the 5s default-announcement window
		}
		time.Sleep(hold)
	}))
}

func floatStr(f float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.3f", f), "0"), ".")
}

func TestDialWeb888PassiveDefault(t *testing.T) {
	frames := [][]byte{kiwiIQFrame(0, [][2]int16{{4096, -4096}})}
	ts := fakeWeb888Server(t, 14074.0, 0.0, frames)
	defer ts.Close()
	host := strings.TrimPrefix(ts.URL, "http://")

	// Public-only dial guard: loopback refused without AllowPrivate.
	if _, err := DialWeb888(context.Background(), Web888Config{Server: host}); err == nil {
		t.Fatal("loopback accepted without AllowPrivate")
	}
	c, err := DialWeb888(context.Background(), Web888Config{Server: host, AllowPrivate: true})
	if err != nil {
		t.Fatalf("passive default dial: %v", err)
	}
	defer c.Close()
	if c.DefaultFreq != 14_074_000 {
		t.Fatalf("DefaultFreq = %d, want 14074000 (server's init.freq)", c.DefaultFreq)
	}
	if got := c.SampleRate(); got != 12000 {
		t.Fatalf("rate = %d, want 12000 parsed from the fork's float form", got)
	}
	buf := make([]complex64, 8)
	n, err := c.ReadSamples(buf)
	if err != nil || n != 1 || buf[0] != complex(0.125, -0.125) {
		t.Fatalf("ReadSamples = %d, %v, %v", n, err, buf[0])
	}
}

func TestDialWeb888NoDefaultAnnounced(t *testing.T) {
	ts := fakeWeb888Server(t, 0, 0, nil)
	defer ts.Close()
	host := strings.TrimPrefix(ts.URL, "http://")
	_, err := DialWeb888(context.Background(), Web888Config{Server: host, AllowPrivate: true})
	if err == nil || !strings.Contains(err.Error(), "no passive default frequency") {
		t.Fatalf("want no-default error, got %v", err)
	}
}

func TestDialWeb888ConfiguredFreq(t *testing.T) {
	frames := [][]byte{kiwiIQFrame(0, [][2]int16{{100, 200}})}
	ts := fakeWeb888Server(t, 14074.0, 0.0, frames)
	defer ts.Close()
	host := strings.TrimPrefix(ts.URL, "http://")
	c, err := DialWeb888(context.Background(), Web888Config{Server: host, Frequency: 7_100_000, AllowPrivate: true})
	if err != nil {
		t.Fatalf("configured dial: %v", err)
	}
	defer c.Close()
	if c.DefaultFreq != 0 {
		t.Fatalf("DefaultFreq = %d, want 0 when the caller named the frequency", c.DefaultFreq)
	}
}
