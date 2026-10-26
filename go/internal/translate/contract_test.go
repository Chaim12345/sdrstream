package translate

import (
	"context"
	"encoding/json"
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
