package translate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type hang struct{}

func (hang) Name() string  { return "gemini" }
func (hang) Model() string { return "h" }
func (hang) Translate(ctx context.Context, _ string) (Output, error) {
	<-ctx.Done()
	return Output{}, ctx.Err()
}

type boom struct{}

func (boom) Name() string                                      { return "gemini" }
func (boom) Model() string                                     { return "b" }
func (boom) Translate(context.Context, string) (Output, error) { panic("provider bug") }

// With a real Gemini provider and a real-looking key, every way of being off
// must leave the network untouched. The server counts hits.
func TestDisabledStatesNeverReachTheNetwork(t *testing.T) {
	var hits int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { atomic.AddInt64(&hits, 1) }))
	defer ts.Close()
	g := &Gemini{APIKey: "AIza-not-a-real-key", BaseURL: ts.URL}
	for name, cfg := range map[string]Config{
		"zero":          {},
		"enabled only":  {Enabled: true},
		"consent only":  {ConsentedProvider: "gemini"},
		"other consent": {Enabled: true, ConsentedProvider: "nvidia"},
		"kind not sent": {Enabled: true, ConsentedProvider: "gemini", AllowedKinds: []Kind{KindDecodedText}},
	} {
		r := New(cfg, g).Translate(context.Background(), KindTranscript, french)
		if r.Translated != "" || r.Original != french || r.Status == StatusTranslated {
			t.Errorf("%s: %+v", name, r)
		}
	}
	// the env path with the key present but no consent
	tr, _ := FromEnv(func(k string) string {
		return map[string]string{"GEMINI_API_KEY": "AIza-not-a-real-key", "SDRSTREAM_TRANSLATE": "on", "SDRSTREAM_TRANSLATE_PROVIDER": "gemini"}[k]
	})
	tr.Translate(context.Background(), KindTranscript, french)
	if n := atomic.LoadInt64(&hits); n != 0 {
		t.Fatalf("%d request(s) reached the network while off", n)
	}
}

// The monitor calls Translate from detached goroutines with a timeout. A hung
// provider must give the context back promptly, keep the original, and not
// count the caller's cancel as a provider failure.
func TestCancelAndTimeoutKeepOriginalAndDoNotPause(t *testing.T) {
	tr := on(hang{}, func(c *Config) { c.CooldownAfterFailures = 1; c.RequestsPerMinute = 100 })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	r := tr.Translate(ctx, KindTranscript, french)
	if time.Since(start) > 2*time.Second || r.Status != StatusError || r.Original != french || r.Translated != "" {
		t.Fatalf("%v %+v", time.Since(start), r)
	}
	// not paused: the next call still reaches the provider (and times out again)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel2()
	if r := tr.Translate(ctx2, KindTranscript, french+" 2"); r.Status != StatusError || strings.Contains(r.Reason, "paused") {
		t.Fatalf("provider paused after a caller timeout: %+v", r)
	}
}

func TestProviderPanicIsAnError(t *testing.T) {
	r := on(boom{}, nil).Translate(context.Background(), KindTranscript, french)
	if r.Status != StatusError || r.Original != french || r.Translated != "" {
		t.Fatalf("%+v", r)
	}
}

// Many monitors translate at once. No race, the ceiling holds, every result
// keeps its own original.
func TestConcurrentCallsKeepOriginalsAndCeiling(t *testing.T) {
	f := &fake{out: Output{Text: "Strong wind tomorrow for all ships in the gulf", SourceLang: "fr"}}
	tr := on(f, func(c *Config) { c.RequestsPerMinute = 5 })
	var wg sync.WaitGroup
	res := make([]Result, 40)
	texts := make([]string, 40)
	for i := range res {
		texts[i] = french + strings.Repeat(" encore", i%8)
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res[i] = tr.Translate(context.Background(), KindTranscript, texts[i])
		}(i)
	}
	wg.Wait()
	for i, r := range res {
		if r.Original != texts[i] {
			t.Fatalf("result %d lost its original", i)
		}
		if r.Status == StatusTranslated && r.Translated == "" {
			t.Fatalf("result %d translated but empty", i)
		}
	}
	if f.calls > 5 {
		t.Fatalf("provider called %d times, ceiling is 5/min", f.calls)
	}
}

// The JSON the UI reads. Field names are the contract.
func TestResultJSONContract(t *testing.T) {
	off := New(Config{}, nil).Translate(context.Background(), KindTranscript, french)
	b, _ := json.Marshal(off)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if m["original"] != french || m["status"] != "disabled" || m["target_lang"] != "en" || m["machine_translated"] != false {
		t.Fatalf("%s", b)
	}
	if _, ok := m["translated"]; ok {
		t.Fatalf("translated present when not translated: %s", b)
	}
	f := &fake{out: Output{Text: "Strong wind tomorrow for all ships in the gulf", SourceLang: "fr"}}
	ok := on(f, nil).Translate(context.Background(), KindTranscript, french)
	b, _ = json.Marshal(ok)
	_ = json.Unmarshal(b, &m)
	for k, v := range map[string]any{"status": "translated", "source_lang": "fr", "target_lang": "en", "provider": "gemini", "model": "fake-1", "machine_translated": true} {
		if m[k] != v {
			t.Errorf("%s = %v, want %v (%s)", k, m[k], v, b)
		}
	}
}

// Short channel numbers and times are normal radio text and must not trip the
// personal-identifier filter; long numbers still do.
func TestFilterDoesNotRefuseOrdinaryRadioNumbers(t *testing.T) {
	for _, s := range []string{
		"Canal 16 veille permanente, heure 1610 UTC, vent 340 degrés force 7",
		"שידור בערוץ 16 בשעה 1610 רוחות מערביות",
	} {
		if ok, why := Eligible(s); !ok {
			t.Errorf("%q refused for %s", s, why)
		}
	}
	for _, s := range []string{"appelez 0612345678", "iban FR7630006000011234567890189"} {
		if ok, _ := Eligible(s); ok {
			t.Errorf("%q passed", s)
		}
	}
}

type stuck struct{ release chan struct{} }

func (stuck) Name() string  { return "gemini" }
func (stuck) Model() string { return "s" }
func (s stuck) Translate(context.Context, string) (Output, error) {
	<-s.release // ignores ctx on purpose
	return Output{}, nil
}

// A provider that ignores its context must not hang callers: each call returns
// at its own deadline, the one waiting for the slot too.
func TestUncooperativeProviderDoesNotHangCallers(t *testing.T) {
	s := stuck{release: make(chan struct{})}
	defer close(s.release)
	tr := on(s, func(c *Config) { c.RequestsPerMinute = 100 })
	for i, txt := range []string{french, french + " bis"} {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		start := time.Now()
		r := tr.Translate(ctx, KindTranscript, txt)
		cancel()
		if time.Since(start) > 2*time.Second || r.Status != StatusError || r.Original != txt {
			t.Fatalf("call %d: %v %+v", i, time.Since(start), r)
		}
	}
}

type failWith struct {
	err   error
	panic any
	calls *int
}

func (failWith) Name() string  { return "gemini" }
func (failWith) Model() string { return "f" }
func (f failWith) Translate(context.Context, string) (Output, error) {
	*f.calls++
	if f.panic != nil {
		panic(f.panic)
	}
	return Output{}, f.err
}

// Neither a panic payload nor a provider error string may reach Result
// (and so the UI/JSON): both can hold request text or secrets.
func TestFailureReasonsExposeNoPayload(t *testing.T) {
	const secret = "SECRET-PAYLOAD-AIzaFAKE"
	for name, p := range map[string]failWith{
		"panic": {panic: secret + french, calls: new(int)},
		"error": {err: errors.New("gemini: " + secret + french), calls: new(int)},
	} {
		tr := on(p, nil)
		r := tr.Translate(context.Background(), KindTranscript, french)
		b, _ := json.Marshal(r)
		if r.Status != StatusError || strings.Contains(string(b), secret) || strings.Contains(r.Reason, "Attention") || r.Original != french {
			t.Fatalf("%s: %s", name, b)
		}
	}
}

// A deadline the provider hits itself while the caller's ctx is alive is a
// provider failure: it must count toward the failure pause. A caller's own
// cancel must not.
func TestProviderDeadlineCountsCallerCancelDoesNot(t *testing.T) {
	n := 0
	tr := on(failWith{err: context.DeadlineExceeded, calls: &n}, func(c *Config) {
		c.RequestsPerMinute = 100
		c.CooldownAfterFailures = 2
	})
	for i := 0; i < 2; i++ {
		tr.Translate(context.Background(), KindTranscript, fmt.Sprintf("%s %d", french, i))
	}
	before := n
	r := tr.Translate(context.Background(), KindTranscript, french+" next")
	if n != before || r.Status == StatusTranslated {
		t.Fatalf("provider-side deadlines did not pause: calls %d->%d %+v", before, n, r)
	}
	m := 0
	tr2 := on(failWith{err: context.Canceled, calls: &m}, func(c *Config) {
		c.RequestsPerMinute = 100
		c.CooldownAfterFailures = 2
	})
	for i := 0; i < 4; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		tr2.Translate(ctx, KindTranscript, fmt.Sprintf("%s %d", french, i))
	}
	r = tr2.Translate(context.Background(), KindTranscript, french+" later")
	if strings.Contains(r.Reason, "paused") || r.Status == StatusRateLimit {
		t.Fatalf("caller cancels paused provider: %+v", r)
	}
}

type counting struct {
	calls   atomic.Int64
	block   chan struct{} // nil: return at once
	started chan struct{}
}

func (*counting) Name() string  { return "gemini" }
func (*counting) Model() string { return "c" }
func (c *counting) Translate(context.Context, string) (Output, error) {
	c.calls.Add(1)
	if c.started != nil {
		select {
		case c.started <- struct{}{}:
		default:
		}
	}
	if c.block != nil {
		<-c.block
	}
	return Output{Text: "Attention all ships, strong wind forecast tomorrow morning in the gulf", SourceLang: "fr"}, nil
}

// An already-cancelled caller must never reach the provider or spend budget,
// even though the provider slot is free (both select cases ready).
func TestPrecancelledCallerNeverDispatches(t *testing.T) {
	p := &counting{}
	tr := on(p, func(c *Config) { c.RequestsPerMinute = 3 })
	for i := 0; i < 200; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		r := tr.Translate(ctx, KindTranscript, fmt.Sprintf("%s %d", french, i))
		if r.Status != StatusError || r.Original == "" {
			t.Fatalf("%d: %+v", i, r)
		}
	}
	st := tr.Stats()
	if p.calls.Load() != 0 || st.Sent != 0 || st.Cancelled != 200 || st.Errors != 0 {
		t.Fatalf("calls=%d stats=%+v", p.calls.Load(), st)
	}
	// budget untouched: the 3 allowed real calls still go through
	for i := 0; i < 3; i++ {
		if r := tr.Translate(context.Background(), KindTranscript, fmt.Sprintf("%s ok %d", french, i)); r.Status == StatusRateLimit {
			t.Fatalf("budget consumed by cancelled calls: %+v", r)
		}
	}
}

// A caller that cancels while queued behind a busy provider is never
// dispatched and takes no budget; the in-flight call is the only Sent.
func TestQueuedCancelNeverDispatches(t *testing.T) {
	p := &counting{block: make(chan struct{}), started: make(chan struct{}, 1)}
	tr := on(p, func(c *Config) { c.RequestsPerMinute = 100 })
	first := make(chan Result, 1)
	go func() { first <- tr.Translate(context.Background(), KindTranscript, french) }()
	<-p.started
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			if r := tr.Translate(ctx, KindTranscript, fmt.Sprintf("%s q%d", french, i)); r.Status != StatusError {
				t.Errorf("queued: %+v", r)
			}
		}(i)
	}
	wg.Wait()
	st := tr.Stats()
	if p.calls.Load() != 1 || st.Sent != 1 || st.Cancelled != 5 {
		t.Fatalf("calls=%d stats=%+v", p.calls.Load(), st)
	}
	close(p.block)
	if r := <-first; r.Status != StatusTranslated {
		t.Fatalf("%+v", r)
	}
}

// Cancel after dispatch is a distinct, honest case: it counts as Sent (the
// request may have left the machine) and as an Error, but not toward the pause.
func TestCancelAfterDispatchCountsAsSent(t *testing.T) {
	p := &counting{block: make(chan struct{}), started: make(chan struct{}, 1)}
	tr := on(p, func(c *Config) { c.CooldownAfterFailures = 1 })
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-p.started; cancel() }()
	r := tr.Translate(ctx, KindTranscript, french)
	st := tr.Stats()
	if r.Status != StatusError || st.Sent != 1 || st.Errors != 1 || st.Cancelled != 0 {
		t.Fatalf("%+v %+v", r, st)
	}
	close(p.block) // let the abandoned provider call finish and free its slot
	for i := 0; len(tr.slot) != 0 && i < 500; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if r2 := tr.Translate(context.Background(), KindTranscript, french+" zz"); r2.Status != StatusTranslated {
		t.Fatalf("caller cancel paused provider: %+v", r2)
	}
}
