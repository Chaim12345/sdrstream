package kiwisdr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestConnectUsesPreflightSelectedWSPath(t *testing.T) {
	for _, tc := range []struct{ selected, want string }{{"/ws/kiwi", "/ws/kiwi/"}, {"legacy", "/"}} {
		t.Run(tc.selected, func(t *testing.T) {
			paths := make(chan string, 1)
			up := websocket.Upgrader{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths <- r.URL.Path
				conn, err := up.Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer conn.Close()
				for i := 0; i < 4; i++ {
					if _, _, err := conn.ReadMessage(); err != nil {
						return
					}
				}
			}))
			defer srv.Close()
			opts := DefaultOptions()
			opts.WSPath = tc.selected
			opts.OpenTimeout = time.Second
			c, err := New(strings.TrimPrefix(srv.URL, "http://"), opts)
			if err != nil {
				t.Fatal(err)
			}
			if err := c.Connect(context.Background()); err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			got := <-paths
			if !strings.HasPrefix(got, tc.want) {
				t.Fatalf("path=%q, want prefix %q", got, tc.want)
			}
		})
	}
}
