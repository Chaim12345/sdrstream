package kiwisdr

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newTest(c *clock) *Preflighter {
	p := NewPreflighter()
	p.Now = c.now
	p.Timeout = 500 * time.Millisecond
	// Tests run against loopback httptest servers, which the SSRF guard
	// (covered separately in internal/common) refuses by default.
	p.AllowPrivate = true
	p.HTTP = &http.Client{Timeout: 500 * time.Millisecond}
	return p
}

func kiwiServer(body string, hits *int) (*httptest.Server, string) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/status" {
			*hits++
			w.Header().Set("Server", "KiwiSDR/1.8")
			w.Write([]byte(body))
			return
		}
		http.NotFound(w, r)
	}))
	return s, strings.TrimPrefix(s.URL, "http://")
}

func TestParseStatus(t *testing.T) {
	s, err := ParseStatus("status=active\nusers=2\nusers_max=4\npreempt=1\next_api=1\n")
	if err != nil || s.Users != 2 || s.UsersMax != 4 || s.Preempt != 1 || s.ExtAPI != "1" {
		t.Fatalf("%+v %v", s, err)
	}
	if _, err := ParseStatus("<html>nope</html>"); err == nil {
		t.Fatal("expected error")
	}
}

func TestBackoffCap(t *testing.T) {
	if BackoffFor(1) != time.Minute || BackoffFor(3) != 4*time.Minute {
		t.Fatal("growth")
	}
	if BackoffFor(40) != 24*time.Hour {
		t.Fatal("cap")
	}
}

func TestOKBusyAndCache(t *testing.T) {
	c := &clock{time.Unix(1_700_000_000, 0)}
	hits := 0
	srv, host := kiwiServer("users=1\nusers_max=4\npreempt=0\n", &hits)
	defer srv.Close()
	p := newTest(c)
	st := p.Check(context.Background(), host)
	if !st.Free() || st.Protocol != "http" || st.Server != "KiwiSDR/1.8" {
		t.Fatalf("%+v", st)
	}
	p.Check(context.Background(), host)
	if hits != 1 {
		t.Fatalf("cache miss, hits=%d", hits)
	}
	c.t = c.t.Add(CacheTTL + time.Second)
	p.Check(context.Background(), host)
	if hits != 2 {
		t.Fatalf("expected refresh after TTL, hits=%d", hits)
	}
	h2 := 0
	bs, bh := kiwiServer("users=4\nusers_max=4\npreempt=0\n", &h2)
	defer bs.Close()
	if v := p.Check(context.Background(), bh).Verdict; v != VerdictBusy {
		t.Fatal(v)
	}
}

func TestDeadBackoffAndDailyBudget(t *testing.T) {
	c := &clock{time.Unix(1_700_000_000, 0)}
	hits := 0
	srv, host := kiwiServer("garbage", &hits)
	defer srv.Close()
	p := newTest(c)
	if v := p.Check(context.Background(), host).Verdict; v != VerdictDead {
		t.Fatal(v)
	}
	// Dead result is cached: no traffic inside the 15 minute window.
	if v := p.Check(context.Background(), host).Verdict; v != VerdictDead || hits != 1 {
		t.Fatalf("want cached dead, got %s hits=%d", v, hits)
	}
	// A cleared cache inside the backoff window must not hit the network.
	p.mu.Lock()
	delete(p.cache, host)
	p.mu.Unlock()
	if v := p.Check(context.Background(), host).Verdict; v != VerdictBackoff || hits != 1 {
		t.Fatalf("want backoff without traffic, got %s hits=%d", v, hits)
	}
	c.t = c.t.Add(CacheTTL + time.Minute)
	p.Check(context.Background(), host)
	c.t = c.t.Add(CacheTTL + 5*time.Minute)
	p.Check(context.Background(), host)
	if hits != 3 {
		t.Fatalf("hits=%d", hits)
	}
	c.t = c.t.Add(CacheTTL + time.Hour)
	if v := p.Check(context.Background(), host).Verdict; v != VerdictBudget || hits != 3 {
		t.Fatalf("4th attempt in 24h must be refused: %s hits=%d", v, hits)
	}
	c.t = c.t.Add(24 * time.Hour)
	p.Check(context.Background(), host)
	if hits != 4 {
		t.Fatalf("budget should reset after 24h, hits=%d", hits)
	}
}

func TestWSPathsOrderAndFallback(t *testing.T) {
	if p := WSPaths("KiwiSDR/1.8", 5); p[0] != "/5/SND" || p[1] != "/ws/kiwi/5/SND" {
		t.Fatal(p)
	}
	if p := WSPaths("nginx", 5); p[0] != "/ws/kiwi/5/SND" {
		t.Fatal(p)
	}
	c := &clock{time.Unix(1_700_000_000, 0)}
	hits := 0
	srv, host := kiwiServer("users=0\nusers_max=2\n", &hits)
	defer srv.Close()
	p := newTest(c)
	p.ProbeWS = true
	var tried []string
	p.Dial = func(_ context.Context, uri string) error {
		tried = append(tried, uri)
		if strings.Contains(uri, "/ws/kiwi/") {
			return nil
		}
		return errors.New("bad handshake")
	}
	st := p.Check(context.Background(), host)
	if !st.Free() || st.WSPath != "/ws/kiwi" || len(tried) != 2 {
		t.Fatalf("%+v %v", st, tried)
	}
	p2 := newTest(c)
	p2.ProbeWS = true
	p2.Dial = func(context.Context, string) error { return errors.New("nope") }
	if v := p2.Check(context.Background(), host).Verdict; v != VerdictDead {
		t.Fatal(v)
	}
}

func TestMD5Auth(t *testing.T) {
	if MD5Auth("") != "" || MD5Auth("abc") != "900150983cd24fb0d6963f7d28e17f72" {
		t.Fatal("md5")
	}
}

func TestEgressDiagnostic(t *testing.T) {
	c := &clock{time.Unix(1_700_000_000, 0)}
	p := newTest(c)
	p.Timeout = 50 * time.Millisecond
	p.HTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	for i := 0; i < EgressBlockedAfter; i++ {
		if d := p.EgressDiagnostic(); d != "" {
			t.Fatalf("too early at %d", i)
		}
		if v := p.Check(context.Background(), "h"+string(rune('a'+i))+".invalid:8073").Verdict; v != VerdictTimeout {
			t.Fatal(v)
		}
	}
	d := p.EgressDiagnostic()
	if !strings.Contains(d, "possible") || !strings.Contains(d, "could also be") {
		t.Fatalf("diag %q", d)
	}
	// One reachable host clears it.
	hits := 0
	srv, host := kiwiServer("users=0\nusers_max=2\n", &hits)
	defer srv.Close()
	p.HTTP = http.DefaultClient
	p.Check(context.Background(), host)
	if p.EgressDiagnostic() != "" {
		t.Fatal("should clear")
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestHostOnlyAcceptsURLs(t *testing.T) {
	cases := map[string]string{
		"http://kiwi.example:8073/":        "kiwi.example:8073",
		"https://kiwi.example:8073/status": "kiwi.example:8073",
		"  KIWI.EXAMPLE:8073  ":            "kiwi.example:8073",
		"kiwi.example:8073":                "kiwi.example:8073",
	}
	for input, want := range cases {
		if got := hostOnly(input); got != want {
			t.Errorf("hostOnly(%q) = %q, want %q", input, got, want)
		}
	}
}
