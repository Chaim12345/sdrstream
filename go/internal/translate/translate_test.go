package translate

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fake struct {
	calls int
	out   Output
	err   error
	seen  []string
}

func (f *fake) Name() string  { return "gemini" }
func (f *fake) Model() string { return "fake-1" }
func (f *fake) Translate(_ context.Context, t string) (Output, error) {
	f.calls++
	f.seen = append(f.seen, t)
	return f.out, f.err
}

const french = "Attention tous les navires, vent fort prévu demain matin dans le golfe"

func on(p Provider, mut func(*Config)) *Translator {
	c := Config{Enabled: true, ConsentedProvider: "gemini"}
	if mut != nil {
		mut(&c)
	}
	return New(c, p)
}

func TestOffByDefaultAndNeedsConsent(t *testing.T) {
	f := &fake{out: Output{Text: "x"}}
	for name, tr := range map[string]*Translator{
		"zero config": New(Config{}, f),
		"no consent":  New(Config{Enabled: true}, f),
		"wrong prov":  New(Config{Enabled: true, ConsentedProvider: "nvidia"}, f),
		"nil prov":    New(Config{Enabled: true, ConsentedProvider: "gemini"}, nil),
	} {
		r := tr.Translate(context.Background(), KindTranscript, french)
		if r.Status != StatusDisabled || r.Original != french || r.Translated != "" {
			t.Errorf("%s: %+v", name, r)
		}
	}
	if f.calls != 0 {
		t.Fatalf("provider called %d times while disabled", f.calls)
	}
}

func TestTranslateKeepsOriginalAndLabels(t *testing.T) {
	f := &fake{out: Output{Text: "Attention all ships, strong wind expected tomorrow morning in the gulf", SourceLang: "fr"}}
	r := on(f, nil).Translate(context.Background(), KindTranscript, french)
	if r.Status != StatusTranslated || r.Original != french || r.SourceLang != "fr" || r.TargetLang != "en" ||
		r.Provider != "gemini" || r.Model != "fake-1" || !r.Machine || r.Translated == "" {
		t.Fatalf("%+v", r)
	}
}

func TestNothingSentForEnglishKindsLengthOrPII(t *testing.T) {
	f := &fake{out: Output{Text: "ok ok ok"}}
	tr := on(f, func(c *Config) { c.MaxChars = 100 })
	for name, tc := range map[string]struct {
		kind Kind
		text string
		want Status
	}{
		"english":     {KindTranscript, "All ships, this is the coast guard with the weather for the area", StatusEnglish},
		"bad kind":    {Kind("metadata"), french, StatusIneligible},
		"too long":    {KindTranscript, strings.Repeat(french+" ", 3), StatusIneligible},
		"too short":   {KindTranscript, "ok", StatusIneligible},
		"email":       {KindTranscript, "Envoyez le rapport à jean.dupont@example.org demain matin", StatusIneligible},
		"phone":       {KindTranscript, "Appelez le numéro 06 12 34 56 78 pour la confirmation", StatusIneligible},
		"long number": {KindTranscript, "Le numéro de compte est 123456789012 pour le virement", StatusIneligible},
		"credential":  {KindTranscript, "Le mot de passe est sécurisé, password changé hier soir", StatusIneligible},
		"coordinates": {KindTranscript, "Position du navire 32.123456, 34.765432 en route vers le port", StatusIneligible},
		"arabic":      {KindTranscript, "تحذير لجميع السفن رياح قوية متوقعة غدا صباحا", StatusTranslated},
		"hebrew":      {KindDecodedText, "אזהרה לכל האוניות רוחות חזקות צפויות מחר בבוקר", StatusTranslated},
	} {
		before := f.calls
		r := tr.Translate(context.Background(), tc.kind, tc.text)
		if r.Status != tc.want {
			t.Errorf("%s: status %s (%s), want %s", name, r.Status, r.Reason, tc.want)
		}
		sent := f.calls != before
		if sent != (tc.want == StatusTranslated) {
			t.Errorf("%s: sent=%v", name, sent)
		}
		if r.Original != tc.text {
			t.Errorf("%s: original altered", name)
		}
	}
}

func TestCacheLimitAndBackoff(t *testing.T) {
	now := time.Unix(1000, 0)
	f := &fake{out: Output{Text: "Strong wind tomorrow morning in the gulf for all ships", SourceLang: "fr"}}
	tr := on(f, func(c *Config) { c.RequestsPerMinute = 2; c.Now = func() time.Time { return now } })
	ctx := context.Background()
	a := tr.Translate(ctx, KindTranscript, french)
	b := tr.Translate(ctx, KindTranscript, french)
	if f.calls != 1 || !b.Cached || a.Translated != b.Translated {
		t.Fatalf("cache: calls=%d %+v", f.calls, b)
	}
	tr.Translate(ctx, KindTranscript, french+" un")
	r := tr.Translate(ctx, KindTranscript, french+" deux")
	if r.Status != StatusRateLimit || f.calls != 2 {
		t.Fatalf("ceiling: %+v calls=%d", r, f.calls)
	}
	now = now.Add(61 * time.Second)
	if r := tr.Translate(ctx, KindTranscript, french+" trois"); r.Status != StatusTranslated {
		t.Fatalf("after window: %+v", r)
	}
}

func TestProviderFailuresPauseAndAreNotCached(t *testing.T) {
	now := time.Unix(2000, 0)
	f := &fake{err: errors.New("boom")}
	tr := on(f, func(c *Config) {
		c.CooldownAfterFailures = 2
		c.Cooldown = time.Minute
		c.RequestsPerMinute = 50
		c.Now = func() time.Time { return now }
	})
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if r := tr.Translate(ctx, KindTranscript, french+strings.Repeat("!", i)); r.Status != StatusError || r.Translated != "" {
			t.Fatalf("%+v", r)
		}
	}
	calls := f.calls
	if r := tr.Translate(ctx, KindTranscript, french+" x"); r.Status != StatusRateLimit || f.calls != calls {
		t.Fatalf("not paused: %+v", r)
	}
	now = now.Add(2 * time.Minute)
	f.err, f.out = nil, Output{Text: "Strong wind tomorrow for all ships in the gulf", SourceLang: "fr"}
	if r := tr.Translate(ctx, KindTranscript, french); r.Status != StatusTranslated {
		t.Fatalf("recovery: %+v", r)
	}
	f.err = &RateLimitError{RetryAfter: 90 * time.Second}
	r := tr.Translate(ctx, KindTranscript, french+" 429")
	if r.Status != StatusRateLimit {
		t.Fatalf("%+v", r)
	}
	if r := tr.Translate(ctx, KindTranscript, french+" 429b"); r.Status != StatusRateLimit {
		t.Fatalf("429 did not pause: %+v", r)
	}
	now = now.Add(2 * time.Minute)
	f.err = ErrQuota
	tr.Translate(ctx, KindTranscript, french+" q")
	if r := tr.Translate(ctx, KindTranscript, french+" q2"); r.Status != StatusRateLimit {
		t.Fatalf("quota did not pause: %+v", r)
	}
}

func TestOutputSanity(t *testing.T) {
	ctx := context.Background()
	for name, out := range map[string]Output{
		"empty":     {Text: "  "},
		"unchanged": {Text: french},
		"huge":      {Text: strings.Repeat("blah ", 400)},
	} {
		f := &fake{out: out}
		r := on(f, nil).Translate(ctx, KindTranscript, french)
		if r.Translated != "" || r.Status == StatusTranslated {
			t.Errorf("%s shown as translation: %+v", name, r)
		}
	}
}

func geminiServer(t *testing.T, key string, status int, body string, hdr map[string]string, got *[]map[string]any) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-goog-api-key") != key || strings.Contains(r.URL.String(), key) {
			t.Errorf("key must be in the header only: %s", r.URL)
		}
		var m map[string]any
		_ = json.NewDecoder(r.Body).Decode(&m)
		*got = append(*got, m)
		for k, v := range hdr {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
}

func TestGeminiProviderOffline(t *testing.T) {
	var got []map[string]any
	inner, _ := json.Marshal(map[string]string{"source_language": "FR", "english": "Strong wind tomorrow in the gulf for all ships"})
	resp, _ := json.Marshal(map[string]any{"candidates": []any{map[string]any{"finishReason": "STOP",
		"content": map[string]any{"parts": []any{map[string]string{"text": string(inner)}}}}}})
	ts := geminiServer(t, "K3Y", 200, string(resp), nil, &got)
	defer ts.Close()
	g := &Gemini{APIKey: "K3Y", BaseURL: ts.URL}
	out, err := g.Translate(context.Background(), french)
	if err != nil || out.SourceLang != "fr" || !strings.Contains(out.Text, "Strong wind") {
		t.Fatalf("%+v %v", out, err)
	}
	// only the text goes out: the one user part is the exact input
	b, _ := json.Marshal(got[0])
	if !strings.Contains(string(b), french) || strings.Contains(string(b), "K3Y") {
		t.Fatalf("request body: %s", b)
	}
}

func TestGeminiErrorsMapAndRedact(t *testing.T) {
	var got []map[string]any
	for name, tc := range map[string]struct {
		status int
		body   string
		hdr    map[string]string
		check  func(error) bool
	}{
		"429 retry": {429, `{"error":{"status":"RESOURCE_EXHAUSTED"}}`, map[string]string{"Retry-After": "30"},
			func(e error) bool { var r *RateLimitError; return errors.As(e, &r) && r.RetryAfter == 30*time.Second }},
		"429 daily": {429, `{"error":{"message":"quota exceeded for requests per day"}}`, nil,
			func(e error) bool { return errors.Is(e, ErrQuota) }},
		"500":     {500, `oops K3Y`, nil, func(e error) bool { return e != nil && !strings.Contains(e.Error(), "K3Y") }},
		"blocked": {200, `{"promptFeedback":{"blockReason":"SAFETY"}}`, nil, func(e error) bool { return e != nil && strings.Contains(e.Error(), "blocked") }},
		"max":     {200, `{"candidates":[{"finishReason":"MAX_TOKENS","content":{"parts":[{"text":"{}"}]}}]}`, nil, func(e error) bool { return e != nil && strings.Contains(e.Error(), "stopped early") }},
		"notjson": {200, `{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"hello"}]}}]}`, nil, func(e error) bool { return e != nil }},
	} {
		ts := geminiServer(t, "K3Y", tc.status, tc.body, tc.hdr, &got)
		_, err := (&Gemini{APIKey: "K3Y", BaseURL: ts.URL}).Translate(context.Background(), french)
		ts.Close()
		if !tc.check(err) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// connection error text must not carry the key
	_, err := (&Gemini{APIKey: "K3Y", BaseURL: "http://127.0.0.1:1/K3Y"}).Translate(context.Background(), french)
	if err == nil || strings.Contains(err.Error(), "K3Y") {
		t.Errorf("dial error: %v", err)
	}
}

func TestFromEnvNeedsEverything(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	for name, m := range map[string]map[string]string{
		"key only":     {"GEMINI_API_KEY": "k"},
		"on, no cons":  {"SDRSTREAM_TRANSLATE": "on", "SDRSTREAM_TRANSLATE_PROVIDER": "gemini", "GEMINI_API_KEY": "k"},
		"wrong cons":   {"SDRSTREAM_TRANSLATE": "on", "SDRSTREAM_TRANSLATE_PROVIDER": "gemini", "SDRSTREAM_TRANSLATE_CONSENT": "nvidia", "GEMINI_API_KEY": "k"},
		"no key":       {"SDRSTREAM_TRANSLATE": "on", "SDRSTREAM_TRANSLATE_PROVIDER": "gemini", "SDRSTREAM_TRANSLATE_CONSENT": "gemini"},
		"provider unk": {"SDRSTREAM_TRANSLATE": "on", "SDRSTREAM_TRANSLATE_PROVIDER": "x", "SDRSTREAM_TRANSLATE_CONSENT": "x", "GEMINI_API_KEY": "k"},
	} {
		tr, note := FromEnv(env(m))
		if tr.Active() || strings.Contains(note, "\"k\"") {
			t.Errorf("%s active: %s", name, note)
		}
	}
	tr, note := FromEnv(env(map[string]string{"SDRSTREAM_TRANSLATE": "on", "SDRSTREAM_TRANSLATE_PROVIDER": "gemini", "SDRSTREAM_TRANSLATE_CONSENT": "gemini", "GEMINI_API_KEY": "secret"}))
	if !tr.Active() || strings.Contains(note, "secret") {
		t.Fatalf("not active: %s", note)
	}
}
