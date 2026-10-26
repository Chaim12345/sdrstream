package ubersdr

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/chaim12345/sdrstream/go/internal/ubersdr/pcmv4/v4enc"
)

// fakeUber is an offline stand-in for an UberSDR: /connection precheck plus a
// /ws that sends a status frame and v4-encoded IQ, and honours a tune message.
// Nothing here touches the network beyond loopback.
func fakeUber(t *testing.T, tunes chan<- map[string]any, gotQuery chan<- string) *httptest.Server {
	t.Helper()
	up := websocket.Upgrader{}
	mux := http.NewServeMux()
	mux.HandleFunc("/connection", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"allowed":true,"allowed_iq_modes":["iq96","iq48"]}`))
	})
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		gotQuery <- r.URL.RawQuery
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.WriteJSON(map[string]any{"type": "status", "mode": "iq48", "sampleRate": 48000})
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
		in := make([]int16, 2*480)
		for i := range in {
			in[i] = int16((i * 37) % 2000)
		}
		for i := 0; i < 3; i++ {
			pkt, err := enc.Encode(in, 48000, 2, 0)
			if err != nil {
				return
			}
			if c.WriteMessage(websocket.BinaryMessage, pkt) != nil {
				return
			}
		}
		// an unknown text frame must surface, not vanish
		_ = c.WriteMessage(websocket.TextMessage, []byte(`{"type":"mystery"}`))
		time.Sleep(300 * time.Millisecond)
	})
	return httptest.NewServer(mux)
}

func TestDialIQOffline(t *testing.T) {
	tunes := make(chan map[string]any, 4)
	q := make(chan string, 1)
	ts := fakeUber(t, tunes, q)
	defer ts.Close()
	s, err := NewServer(strings.TrimPrefix(ts.URL, "http://"), "")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	id := NewSessionID()
	res, err := s.Check(ctx, id)
	if err != nil || !res.Allowed || !res.AllowsKHz(48) || res.AllowsKHz(384) {
		t.Fatalf("check: %+v %v", res, err)
	}
	c, err := s.DialIQ(ctx, id, 7_100_000, 48, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, want := range []string{"frequency=7100000", "mode=iq48", "version=4", "format=pcm-zstd"} {
		if got := <-q; !strings.Contains(got, want) {
			t.Fatalf("query %q lacks %q", got, want)
		}
		break
	}
	if err := c.Tune(7_200_000, 48); err != nil {
		t.Fatal(err)
	}
	var iq, status, text int
	for i := 0; i < 5; i++ {
		m, err := c.ReadIdle(2 * time.Second)
		if err != nil {
			t.Fatal(err)
		}
		switch m.Kind {
		case MsgIQ:
			iq++
			if m.SampleRate != 48000 || len(m.Samples) != 960 {
				t.Fatalf("iq frame: %d Hz %d samples", m.SampleRate, len(m.Samples))
			}
		case MsgStatus:
			status++
		case MsgText:
			text++
		default:
			t.Fatalf("unexpected kind %v %s", m.Kind, m.Text)
		}
	}
	if iq != 3 || status != 1 || text != 1 {
		t.Fatalf("iq=%d status=%d text=%d", iq, status, text)
	}
	m := <-tunes
	if m["type"] != "tune" || m["mode"] != "iq48" || m["frequency"].(float64) != 7_200_000 {
		t.Fatalf("tune message %v", m)
	}
}

func TestDialHookAndRedirectAndRedaction(t *testing.T) {
	tunes := make(chan map[string]any, 4)
	q := make(chan string, 1)
	ts := fakeUber(t, tunes, q)
	defer ts.Close()
	host := strings.TrimPrefix(ts.URL, "http://")

	// A refusing dial hook blocks both the precheck and the socket, and the
	// password never appears in the error.
	var calls int
	s, _ := NewServer(host, "s3cret-pw")
	s.NetDialContext = func(ctx context.Context, n, a string) (net.Conn, error) {
		calls++
		return nil, errors.New("refused by guard for s3cret-pw")
	}
	if _, err := s.Check(context.Background(), NewSessionID()); err == nil || strings.Contains(err.Error(), "s3cret-pw") {
		t.Fatalf("check err = %v", err)
	}
	if _, err := s.DialIQ(context.Background(), NewSessionID(), 7_000_000, 48, 0); err == nil || strings.Contains(err.Error(), "s3cret-pw") {
		t.Fatalf("dial err = %v", err)
	}
	if calls < 2 {
		t.Fatalf("hook called %d times", calls)
	}

	// A redirect is not followed.
	hit := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
	defer target.Close()
	rd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer rd.Close()
	s2, _ := NewServer(strings.TrimPrefix(rd.URL, "http://"), "")
	_, _ = s2.Check(context.Background(), NewSessionID())
	if hit {
		t.Fatal("redirect was followed")
	}
}
