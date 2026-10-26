package iqsrc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/chaim12345/sdrstream/go/internal/ubersdr/pcmv4/v4enc"
)

// loopback-only fake UberSDR; no external contact.
func fakeUberServer(t *testing.T, modes string, status int, tunes chan<- map[string]any) *httptest.Server {
	up := websocket.Upgrader{}
	mux := http.NewServeMux()
	mux.HandleFunc("/connection", func(w http.ResponseWriter, r *http.Request) {
		if status != 0 {
			w.WriteHeader(status)
		}
		w.Write([]byte(`{"allowed":true,"allowed_iq_modes":` + modes + `,"max_session_time":300}`))
	})
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.WriteJSON(map[string]any{"type": "status", "mode": "iq96", "sampleRate": 96000})
		go func() {
			for {
				var m map[string]any
				if c.ReadJSON(&m) != nil {
					return
				}
				tunes <- m
			}
		}()
		enc := v4enc.New()
		in := make([]int16, 2*100)
		for i := range in {
			in[i] = int16(i * 100)
		}
		for i := 0; i < 2; i++ {
			pkt, _ := enc.Encode(in, 96000, 2, 0)
			if c.WriteMessage(websocket.BinaryMessage, pkt) != nil {
				return
			}
		}
		time.Sleep(400 * time.Millisecond)
	})
	return httptest.NewServer(mux)
}

func TestDialUberOffline(t *testing.T) {
	tunes := make(chan map[string]any, 2)
	ts := fakeUberServer(t, `["iq48","iq96"]`, 0, tunes)
	defer ts.Close()
	ctx := context.Background()

	// SSRF guard on by default: loopback refused before any request.
	if _, err := DialUber(ctx, UberConfig{URL: ts.URL, Frequency: 7_000_000, SampleRate: 96000}); err == nil {
		t.Fatal("loopback accepted without AllowPrivate")
	}

	c, err := DialUber(ctx, UberConfig{URL: ts.URL, Frequency: 7_000_000, SampleRate: 200000, AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.SampleRate() != 96000 || c.MaxSessionTime() != 300*time.Second {
		t.Fatalf("rate %d max %v", c.SampleRate(), c.MaxSessionTime())
	}
	buf := make([]complex64, 60)
	total := 0
	for total < 200 {
		n, err := c.ReadSamples(buf)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 || n > 60 {
			t.Fatalf("n=%d", n)
		}
		total += n
	}
	if total != 200 {
		t.Fatalf("total %d (held remainder across calls)", total)
	}
	if err := c.SetFrequency(7_050_000); err != nil {
		t.Fatal(err)
	}
	m := <-tunes
	if m["frequency"].(float64) != 7_050_000 || m["mode"] != "iq96" {
		t.Fatalf("%v", m)
	}
}

func TestDialUberRefusals(t *testing.T) {
	tunes := make(chan map[string]any, 1)
	none := fakeUberServer(t, `[]`, 0, tunes)
	defer none.Close()
	if _, err := DialUber(context.Background(), UberConfig{URL: none.URL, SampleRate: 48000, AllowPrivate: true}); err == nil || !strings.Contains(err.Error(), "no wide IQ mode") {
		t.Fatalf("err %v", err)
	}
	rl := fakeUberServer(t, `["iq48"]`, 429, tunes)
	defer rl.Close()
	if _, err := DialUber(context.Background(), UberConfig{URL: rl.URL, SampleRate: 48000, AllowPrivate: true}); err != ErrUberRateLimited {
		t.Fatalf("err %v", err)
	}
}

func TestUberSessionTimeEnforced(t *testing.T) {
	tunes := make(chan map[string]any, 1)
	ts := fakeUberServer(t, `["iq96"]`, 0, tunes)
	defer ts.Close()
	c, err := DialUber(context.Background(), UberConfig{URL: ts.URL, SampleRate: 96000, AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.maxTime = 30 * time.Millisecond // as if the server had announced 30ms
	time.Sleep(60 * time.Millisecond)
	buf := make([]complex64, 16)
	if _, err := c.ReadSamples(buf); err != ErrUberSessionTime {
		t.Fatalf("err = %v, want ErrUberSessionTime", err)
	}
}
