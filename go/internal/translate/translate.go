// Package translate turns non-English radio text and transcripts into English
// for display next to the original. It never replaces the original, labels every
// result as machine translation with its provider, model and source language,
// and is OFF unless an operator turns it on and names the provider they have
// consented to send content to.
//
// What may be sent: only the text of a decoded message or a transcript (Kind),
// never metadata, credentials, receiver addresses or images. Public reception
// does not make content safe to forward: the provider's terms (Gemini unpaid
// quota: Google may use content to improve products and humans may read it, and
// the terms say not to submit sensitive, confidential or personal information)
// are honored by an eligibility filter that refuses text that looks like it
// carries personal identifiers. A refused text gets StatusIneligible, not a
// translation.
package translate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Kind says what a text is. Only the kinds in Config.AllowedKinds are sent.
type Kind string

const (
	KindTranscript  Kind = "transcript"
	KindDecodedText Kind = "decoded_text"
)

// Status of one translation attempt.
type Status string

const (
	StatusTranslated Status = "translated"
	StatusEnglish    Status = "already_english" // judged English; nothing sent
	StatusUnchanged  Status = "unchanged"       // provider returned the input
	StatusDisabled   Status = "disabled"        // off, or provider not consented
	StatusIneligible Status = "ineligible"      // content/kind filter refused; nothing sent
	StatusRateLimit  Status = "rate_limited"    // local budget or provider 429; nothing cached
	StatusRejected   Status = "rejected"        // output failed a sanity check; not shown as translation
	StatusError      Status = "error"
)

// Result is what a caller stores and shows. Original is always the input
// verbatim. Translated is empty unless Status is StatusTranslated.
type Result struct {
	Original   string `json:"original"`
	Translated string `json:"translated,omitempty"`
	SourceLang string `json:"source_lang,omitempty"` // as reported by the provider; may be empty/uncertain
	TargetLang string `json:"target_lang"`
	Provider   string `json:"provider,omitempty"`
	Model      string `json:"model,omitempty"`
	Status     Status `json:"status"`
	Reason     string `json:"reason,omitempty"` // why not translated, human readable, no secrets
	Cached     bool   `json:"cached,omitempty"`
	Machine    bool   `json:"machine_translated"` // always true when Translated is set
}

// Output is a provider's answer.
type Output struct {
	Text       string
	SourceLang string
}

// Provider translates one text to English.
type Provider interface {
	Name() string
	Model() string
	Translate(ctx context.Context, text string) (Output, error)
}

// Typed provider errors that change what the Translator does.
var (
	// ErrRateLimited: the provider asked us to slow down. RetryAfter, when
	// known, is carried by RateLimitError.
	ErrRateLimited = errors.New("translate: provider rate limited")
	// ErrQuota: the free quota is used up for now; stop sending for a while.
	ErrQuota = errors.New("translate: provider quota exhausted")
)

// RateLimitError wraps ErrRateLimited with the server's Retry-After.
type RateLimitError struct{ RetryAfter time.Duration }

func (e *RateLimitError) Error() string { return ErrRateLimited.Error() }
func (e *RateLimitError) Unwrap() error { return ErrRateLimited }

// Config controls a Translator. The zero value is OFF.
type Config struct {
	// Enabled is the master switch. Default false.
	Enabled bool
	// ConsentedProvider is the provider name the operator has agreed to send
	// content to. A Translator whose provider name differs stays disabled, so a
	// saved key alone never starts sending.
	ConsentedProvider string
	// AllowedKinds limits what may be sent; empty means transcript and decoded text.
	AllowedKinds []Kind
	// MaxChars is the longest text sent (default 2000). Longer texts are
	// refused, not truncated, so a partial text is never passed off as whole.
	MaxChars int
	// MinLetters: texts with fewer letters are not worth a call (default 8).
	MinLetters int
	// RequestsPerMinute is the local ceiling (default 6, well under free quotas).
	RequestsPerMinute int
	// CacheSize is the number of results kept (default 512).
	CacheSize int
	// CooldownAfterFailures: consecutive provider failures before pausing, and
	// Cooldown how long (defaults 3, 5 minutes).
	CooldownAfterFailures int
	Cooldown              time.Duration
	// Now is for tests.
	Now func() time.Time
}

// Stats are counters for status screens; no content.
type Stats struct {
	Sent, Cached, Translated, Skipped, Errors, RateLimited uint64
}

// Translator is safe for concurrent use; provider calls are serialized.
type Translator struct {
	cfg  Config
	prov Provider

	mu        sync.Mutex // guards everything below
	cache     map[string]Result
	order     []string
	stamps    []time.Time // recent send times, for the per-minute ceiling
	fails     int
	pausedTil time.Time
	stats     Stats
	callMu    sync.Mutex
}

// New returns a Translator. A nil provider is allowed and means disabled.
func New(cfg Config, p Provider) *Translator {
	if cfg.MaxChars <= 0 {
		cfg.MaxChars = 2000
	}
	if cfg.MinLetters <= 0 {
		cfg.MinLetters = 8
	}
	if cfg.RequestsPerMinute <= 0 {
		cfg.RequestsPerMinute = 6
	}
	if cfg.CacheSize <= 0 {
		cfg.CacheSize = 512
	}
	if cfg.CooldownAfterFailures <= 0 {
		cfg.CooldownAfterFailures = 3
	}
	if cfg.Cooldown <= 0 {
		cfg.Cooldown = 5 * time.Minute
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if len(cfg.AllowedKinds) == 0 {
		cfg.AllowedKinds = []Kind{KindTranscript, KindDecodedText}
	}
	return &Translator{cfg: cfg, prov: p, cache: map[string]Result{}}
}

// Stats returns a copy of the counters.
func (t *Translator) Stats() Stats { t.mu.Lock(); defer t.mu.Unlock(); return t.stats }

// Active reports whether Translate may send content at all.
func (t *Translator) Active() bool {
	return t.cfg.Enabled && t.prov != nil && t.cfg.ConsentedProvider != "" &&
		strings.EqualFold(t.cfg.ConsentedProvider, t.prov.Name())
}

func (t *Translator) result(text string, st Status, reason string) Result {
	r := Result{Original: text, TargetLang: "en", Status: st, Reason: reason}
	if t.prov != nil {
		r.Provider, r.Model = t.prov.Name(), t.prov.Model()
	}
	return r
}

// Translate returns the English translation of text, or a Result saying why
// there is none. It never returns an error: every outcome is a labeled Result.
func (t *Translator) Translate(ctx context.Context, kind Kind, text string) Result {
	if !t.Active() {
		return t.count(t.result(text, StatusDisabled, "translation is off or the provider is not consented"))
	}
	if !t.kindAllowed(kind) {
		return t.count(t.result(text, StatusIneligible, "content kind "+string(kind)+" is not sent"))
	}
	if n := len([]rune(text)); n > t.cfg.MaxChars {
		return t.count(t.result(text, StatusIneligible, fmt.Sprintf("longer than %d characters; not sent", t.cfg.MaxChars)))
	}
	if letters(text) < t.cfg.MinLetters {
		return t.count(t.result(text, StatusIneligible, "too little text to translate"))
	}
	if LooksEnglish(text) {
		return t.count(t.result(text, StatusEnglish, "judged English locally; nothing sent"))
	}
	if ok, why := Eligible(text); !ok {
		return t.count(t.result(text, StatusIneligible, "looks like it contains "+why+"; not sent to a third-party service"))
	}

	key := cacheKey(t.prov, text)
	t.mu.Lock()
	if r, ok := t.cache[key]; ok {
		t.stats.Cached++
		t.mu.Unlock()
		r.Cached = true
		return r
	}
	now := t.cfg.Now()
	if now.Before(t.pausedTil) {
		t.stats.RateLimited++
		t.mu.Unlock()
		return t.result(text, StatusRateLimit, "provider paused after repeated failures; retry later")
	}
	if !t.takeSlot(now) {
		t.stats.RateLimited++
		t.mu.Unlock()
		return t.result(text, StatusRateLimit, "local request budget used; retry later")
	}
	t.stats.Sent++
	t.mu.Unlock()

	t.callMu.Lock()
	out, err := t.prov.Translate(ctx, text)
	t.callMu.Unlock()
	if err != nil {
		return t.failed(text, err)
	}
	return t.accept(key, text, out)
}

func (t *Translator) count(r Result) Result {
	t.mu.Lock()
	t.stats.Skipped++
	t.mu.Unlock()
	return r
}

// takeSlot enforces RequestsPerMinute over a sliding minute. Caller holds mu.
func (t *Translator) takeSlot(now time.Time) bool {
	cut := now.Add(-time.Minute)
	keep := t.stamps[:0]
	for _, s := range t.stamps {
		if s.After(cut) {
			keep = append(keep, s)
		}
	}
	t.stamps = keep
	if len(t.stamps) >= t.cfg.RequestsPerMinute {
		return false
	}
	t.stamps = append(t.stamps, now)
	return true
}

func (t *Translator) failed(text string, err error) Result {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.cfg.Now()
	var rl *RateLimitError
	switch {
	case errors.Is(err, ErrQuota):
		t.pausedTil = now.Add(t.cfg.Cooldown)
		t.stats.RateLimited++
		return t.result(text, StatusRateLimit, "free quota exhausted; paused")
	case errors.Is(err, ErrRateLimited):
		wait := t.cfg.Cooldown
		if errors.As(err, &rl) && rl.RetryAfter > 0 {
			wait = rl.RetryAfter
		}
		t.pausedTil = now.Add(wait)
		t.stats.RateLimited++
		return t.result(text, StatusRateLimit, "provider asked to slow down; paused")
	}
	t.fails++
	t.stats.Errors++
	if t.fails >= t.cfg.CooldownAfterFailures {
		t.pausedTil = now.Add(t.cfg.Cooldown)
		t.fails = 0
	}
	return t.result(text, StatusError, err.Error())
}

func (t *Translator) accept(key, text string, out Output) Result {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.fails = 0
	got := strings.TrimSpace(out.Text)
	r := t.result(text, StatusTranslated, "")
	r.SourceLang = out.SourceLang
	switch {
	case got == "":
		r.Status, r.Reason = StatusRejected, "provider returned no text"
	case strings.EqualFold(got, strings.TrimSpace(text)):
		r.Status, r.Reason = StatusUnchanged, "provider returned the text unchanged"
	case implausibleLength(text, got):
		r.Status, r.Reason = StatusRejected, "translation length is implausible for the input; not shown"
	default:
		r.Translated, r.Machine = got, true
		t.stats.Translated++
	}
	if r.Status == StatusTranslated || r.Status == StatusUnchanged {
		t.put(key, r)
	}
	return r
}

func (t *Translator) put(key string, r Result) {
	if len(t.order) >= t.cfg.CacheSize {
		delete(t.cache, t.order[0])
		t.order = t.order[1:]
	}
	t.cache[key] = r
	t.order = append(t.order, key)
}

func (t *Translator) kindAllowed(k Kind) bool {
	for _, a := range t.cfg.AllowedKinds {
		if a == k {
			return true
		}
	}
	return false
}

func cacheKey(p Provider, text string) string {
	h := sha256.Sum256([]byte(p.Name() + "\x00" + p.Model() + "\x00" + text))
	return hex.EncodeToString(h[:])
}

func implausibleLength(in, out string) bool {
	a, b := len([]rune(in)), len([]rune(out))
	return b > 6*a+40 || (a > 40 && b*8 < a)
}
