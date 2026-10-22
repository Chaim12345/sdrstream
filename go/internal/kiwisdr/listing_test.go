package kiwisdr

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

const sampleListing = `// header
var kiwisdr_com =
[
	{"id":"a","status":"active","offline":"no","name":"One","users":"1","users_max":"4","gps":"(44.5, -0.8)","sw_version":"KiwiSDR_v1.902","url":"http://Kiwi.Example.com:8074"},
	{"id":"b","status":"active","offline":"no","name":"Dup","users":"0","users_max":"4","url":"http://kiwi.example.com:8074/"},
	{"id":"c","status":"active","offline":"yes","name":"Down","users":"0","users_max":"4","url":"http://down.example.com"},
	{"id":"d","status":"active","offline":"no","name":"Full","users":"4","users_max":"4","url":"https://full.example.com"},
	{"id":"e","name":"NoURL","url":""},
	{"id":"f","status":"active","offline":"no","name":"OfflineFirst","url":"http://swap.example.com:8073","offline":"yes"},
	{"id":"g","status":"active","offline":"no","name":"OnlineLater","users":"0","users_max":"2","url":"http://swap.example.com:8073"},
]
;
`

func TestParseListingDedupeAndFields(t *testing.T) {
	got, raw, err := ParseListing(sampleListing)
	if err != nil {
		t.Fatal(err)
	}
	if raw != 7 {
		t.Errorf("raw entries = %d, want 7", raw)
	}
	byKey := map[string]Receiver{}
	for _, r := range got {
		byKey[r.Key()] = r
	}
	if len(got) != 4 {
		t.Fatalf("got %d receivers %v, want 4 (dup, empty url collapsed)", len(got), got)
	}
	one := byKey["kiwi.example.com:8074"]
	if one.Name != "One" || one.Lat != 44.5 || one.Lon != -0.8 || one.FreeSlots() != 3 || !one.Usable() {
		t.Errorf("bad parse: %+v", one)
	}
	if d := byKey["down.example.com:80"]; d.Port != 80 || d.Usable() {
		t.Errorf("offline default port: %+v", d)
	}
	if f := byKey["full.example.com:443"]; !f.Secure || f.Usable() {
		t.Errorf("full https: %+v", f)
	}
	if s := byKey["swap.example.com:8073"]; s.Name != "OnlineLater" {
		t.Errorf("online duplicate should replace offline first: %+v", s)
	}
	if one.Kind() != "kiwi" {
		t.Error("kind")
	}
}

func TestParseListingRejectsGarbage(t *testing.T) {
	for _, in := range []string{"", "nothing here", "var kiwisdr_com = {"} {
		if _, _, err := ParseListing(in); err == nil {
			t.Errorf("expected error for %q", in)
		}
	}
}

func TestLoaderFetchesOncePerHourAndServesStale(t *testing.T) {
	hits := 0
	fail := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.Header.Get("User-Agent") == "" {
			t.Error("no user agent")
		}
		if fail {
			http.Error(w, "boom", 500)
			return
		}
		w.Write([]byte(sampleListing))
	}))
	defer srv.Close()
	now := time.Now()
	dir := t.TempDir()
	l := NewLoader(dir)
	l.URL = srv.URL
	l.Now = func() time.Time { return now }
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, _, err := l.Load(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if hits != 1 {
		t.Fatalf("hits=%d want 1 within TTL", hits)
	}
	// A second process (new loader) reuses the disk copy while fresh.
	st, _ := os.Stat(filepath.Join(dir, "kiwisdr_com.js"))
	l2 := NewLoader(dir)
	l2.URL = srv.URL
	l2.Now = func() time.Time { return st.ModTime().Add(10 * time.Minute) }
	if _, _, err := l2.Load(ctx); err != nil || hits != 1 {
		t.Fatalf("disk cache not used: err=%v hits=%d", err, hits)
	}
	// After the TTL it refetches; if that fails the stale copy is served.
	now = now.Add(61 * time.Minute)
	fail = true
	got, _, err := l.Load(ctx)
	if err != nil || len(got) == 0 {
		t.Fatalf("stale fallback failed: %v", err)
	}
	if hits != 2 {
		t.Fatalf("hits=%d want 2", hits)
	}
}

func refusingServer(t *testing.T, frame string) *httptest.Server {
	up := websocket.Upgrader{}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.WriteMessage(websocket.BinaryMessage, append([]byte("MSG\x00"), frame...))
		time.Sleep(300 * time.Millisecond)
	}))
}

func TestRecordReportsSlotLimitAndPasswordRefusal(t *testing.T) {
	cases := []struct {
		frame string
		is    error
	}{
		{"too_busy=4", ErrBusy},
		{"badp=1", ErrBadPassword},
		{"down=", ErrDown},
	}
	for _, tc := range cases {
		srv := refusingServer(t, tc.frame)
		opts := DefaultOptions()
		opts.Reconnect = false
		c, err := New(srv.URL, opts)
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Connect(context.Background()); err != nil {
			t.Fatal(err)
		}
		_, _, err = c.Record(context.Background(), 2*time.Second)
		if !errors.Is(err, tc.is) {
			t.Errorf("%s: err=%v want %v", tc.frame, err, tc.is)
		}
		var be *BusyError
		if tc.is == ErrBusy && (!errors.As(err, &be) || be.Slots != 4) {
			t.Errorf("busy slots: %v", err)
		}
		c.Close()
		srv.Close()
	}
}

func TestBadpZeroIsNotARefusal(t *testing.T) {
	srv := refusingServer(t, "badp=0")
	defer srv.Close()
	opts := DefaultOptions()
	opts.Reconnect = false
	c, _ := New(srv.URL, opts)
	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, _, err := c.Record(context.Background(), 150*time.Millisecond)
	if errors.Is(err, ErrBadPassword) {
		t.Fatal("badp=0 treated as refusal")
	}
}
