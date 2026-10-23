package gallery

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Stalled-HTTP cancellation: the server holds the response forever; a
// cancelled FetchContext must return promptly with the context error
// instead of waiting out the client timeout.
func TestFetchContextCancelStopsStalledHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // stall until the client gives up
	}))
	defer srv.Close()
	items := []Item{{Kind: "audio", Mode: "SSTV", Name: "x.wav", URL: srv.URL + "/x.wav"}}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	start := time.Now()
	_, err := FetchContext(ctx, items, t.TempDir(), 30*time.Second, 1<<20, false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if el := time.Since(start); el > 5*time.Second {
		t.Fatalf("stalled fetch took %v after cancel", el)
	}
}
