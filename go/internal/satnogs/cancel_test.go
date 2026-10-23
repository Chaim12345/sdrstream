package satnogs

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A cancelled BetweenContext stops a stalled request promptly; the
// retry backoff does not stretch the wait.
func TestBetweenContextCancelStopsStalledHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()
	oldAPI := networkAPI
	networkAPI = srv.URL
	oldCache := CacheDir
	CacheDir = t.TempDir()
	t.Cleanup(func() { networkAPI = oldAPI; CacheDir = oldCache })

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	start := time.Now()
	_, err := BetweenContext(ctx, time.Now().Add(-time.Hour), time.Now(), 0, 5, 30*time.Second)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if el := time.Since(start); el > 5*time.Second {
		t.Fatalf("stalled request took %v after cancel", el)
	}
}
