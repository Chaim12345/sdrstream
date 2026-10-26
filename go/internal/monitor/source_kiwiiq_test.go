package monitor

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// loopback-only fake KiwiSDR IQ stream; no external contact. Streams a 1kHz
// tone in IQ so the demodulated audio has measurable energy.
func fakeKiwiIQ(t *testing.T) *httptest.Server {
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
		if err := c.WriteMessage(websocket.TextMessage, []byte("MSG sample_rate=12000")); err != nil {
			return
		}
		var seq byte
		phase := 0.0
		for {
			payload := make([]byte, 0, 512)
			for i := 0; i < 128; i++ {
				phase += 2 * math.Pi * 1000 / 12000
				iv := int16(12000 * math.Cos(phase))
				qv := int16(12000 * math.Sin(phase))
				payload = append(payload, byte(iv>>8), byte(iv), byte(qv>>8), byte(qv))
			}
			frame := []byte{'S', 'N', 'D', 0x08, seq, 0, 0, 0, 4, 244, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
			frame = append(frame, payload...)
			if err := c.WriteMessage(websocket.BinaryMessage, frame); err != nil {
				return
			}
			seq++
			time.Sleep(5 * time.Millisecond)
		}
	}))
}

func TestKiwiIQSourceListenOffline(t *testing.T) {
	ts := fakeKiwiIQ(t)
	defer ts.Close()
	host := strings.TrimPrefix(ts.URL, "http://")

	// Unknown kinds still fail; kiwi-iq is now registered.
	if _, err := SourceFor(context.Background(), "kiwi-iq-nope", host, ""); err == nil {
		t.Fatal("unknown kind accepted")
	}
	// Public-guard default: loopback refused.
	if _, err := SourceFor(context.Background(), "kiwi-iq", host, ""); err == nil {
		t.Fatal("loopback accepted without explicit local permission")
	}
	src, err := SourceForLocal(context.Background(), "kiwi-iq", host, "")
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	if _, _, _, ok := src.Spectrum(); ok {
		t.Fatal("IQ-mode source must report no spectrum")
	}
	audio, rate, err := src.Listen(context.Background(), 7_100_000, "usb", 250*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if rate != 12000 {
		t.Fatalf("rate %d", rate)
	}
	if len(audio) != 3000 {
		t.Fatalf("samples %d, want 3000", len(audio))
	}
	var energy float64
	for _, v := range audio {
		energy += v * v
	}
	if energy == 0 {
		t.Fatal("silent audio from a driven IQ tone")
	}
}
