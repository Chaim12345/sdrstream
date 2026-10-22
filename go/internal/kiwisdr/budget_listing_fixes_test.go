package kiwisdr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// mutableKiwi serves /status whose body can be swapped mid-test.
func mutableKiwi(body *atomic.Value, hits *int) (*httptest.Server, string) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/status" {
			*hits++
			w.Header().Set("Server", "KiwiSDR/1.8")
			w.Write([]byte(body.Load().(string)))
			return
		}
		http.NotFound(w, r)
	}))
	return s, strings.TrimPrefix(s.URL, "http://")
}

// A host that finally answers after burning attempts must get its daily
// budget back: the budget exists to stop hammering dead hosts, not to
// punish one that recovered.
func TestBudgetResetsOnSuccess(t *testing.T) {
	c := &clock{time.Unix(1_700_000_000, 0)}
	var body atomic.Value
	body.Store("garbage")
	hits := 0
	srv, host := mutableKiwi(&body, &hits)
	defer srv.Close()
	p := newTest(c)
	ctx := context.Background()

	// Burn two attempts on a dead host.
	for i := 0; i < 2; i++ {
		if v := p.Check(ctx, host).Verdict; v != VerdictDead {
			t.Fatalf("attempt %d: %s", i, v)
		}
		c.t = c.t.Add(CacheTTL + BackoffFor(i+1) + time.Minute)
	}
	// Host recovers: the success must clear the attempt history.
	body.Store("users=0\nusers_max=2\n")
	if v := p.Check(ctx, host).Verdict; v != VerdictOK {
		t.Fatalf("recovery: %s", v)
	}
	// It dies again. With the budget reset we get MaxAttemptsPerDay fresh
	// attempts before VerdictBudget; without it the two pre-recovery
	// attempts would still count.
	body.Store("garbage")
	for i := 0; i < MaxAttemptsPerDay; i++ {
		c.t = c.t.Add(CacheTTL + BackoffFor(i+1) + time.Minute)
		if v := p.Check(ctx, host).Verdict; v != VerdictDead {
			t.Fatalf("post-recovery attempt %d: %s", i, v)
		}
	}
	c.t = c.t.Add(CacheTTL + BackoffFor(MaxAttemptsPerDay+1) + time.Minute)
	if v := p.Check(ctx, host).Verdict; v != VerdictBudget {
		t.Fatalf("want budget after %d fresh attempts, got %s", MaxAttemptsPerDay, v)
	}
	if hits != 2+1+MaxAttemptsPerDay {
		t.Fatalf("hits=%d", hits)
	}
}

// A failing mirror must not be re-fetched on every Load call: after a
// failure the loader backs off before spending another network attempt.
func TestListingFailureBackoff(t *testing.T) {
	fetches := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches++
		http.Error(w, "mirror down", http.StatusBadGateway)
	}))
	defer srv.Close()
	now := time.Unix(1_700_000_000, 0)
	l := &Loader{URL: srv.URL, TTL: ListingTTL, Now: func() time.Time { return now }}
	ctx := context.Background()
	if _, _, err := l.Load(ctx); err == nil {
		t.Fatal("first fetch must fail")
	}
	for i := 0; i < 5; i++ {
		if _, _, err := l.Load(ctx); err == nil || !strings.Contains(err.Error(), "backing off") {
			t.Fatalf("call %d: want backoff error, got %v", i, err)
		}
	}
	if fetches != 1 {
		t.Fatalf("retry storm: %d fetches for 6 Load calls", fetches)
	}
	// After the backoff window a retry is allowed; if the mirror is back
	// the failure count resets.
	now = now.Add(RetryBase + time.Second)
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches++
		w.Write([]byte("var kiwisdr_com = [];"))
	})
	if _, _, err := l.Load(ctx); err != nil {
		t.Fatalf("retry after backoff: %v", err)
	}
	if l.failures != 0 {
		t.Fatalf("failures not reset: %d", l.failures)
	}
}
