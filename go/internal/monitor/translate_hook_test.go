package monitor

import (
	"context"
	"testing"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/translate"
)

type fakeTranslator struct{}

func (f *fakeTranslator) Name() string  { return "fake" }
func (f *fakeTranslator) Model() string { return "t0" }
func (f *fakeTranslator) Translate(ctx context.Context, text string) (translate.Output, error) {
	return translate.Output{Text: "translated: " + text, SourceLang: "es"}, nil
}

// A guarded voice transcript is translated asynchronously: the labeled
// result arrives through OnTranslation with the original preserved.
func TestMaybeTranslateDeliversAsyncResult(t *testing.T) {
	tr := translate.New(translate.Config{Enabled: true, ConsentedProvider: "fake"}, &fakeTranslator{})
	got := make(chan translate.Result, 1)
	m := New(Options{Translator: tr, OnTranslation: func(a Activity, r translate.Result) { got <- r }})
	act := Activity{ID: 7, Kind: "voice", Detail: "hola que tal amigos del mar"}
	m.maybeTranslate(act)
	select {
	case r := <-got:
		if r.Status != translate.StatusTranslated || r.Translated == "" {
			t.Fatalf("status=%s reason=%q, want translated", r.Status, r.Reason)
		}
		if r.Original != act.Detail {
			t.Fatalf("original changed: %q", r.Original)
		}
		if !r.Machine {
			t.Fatal("machine_translated flag must be set on a translation")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no translation delivered")
	}
}

// An inactive translator (off / no consent) must never fire the callback.
func TestMaybeTranslateOffWhenInactive(t *testing.T) {
	tr := translate.New(translate.Config{}, &fakeTranslator{})
	called := false
	m := New(Options{Translator: tr, OnTranslation: func(a Activity, r translate.Result) { called = true }})
	m.maybeTranslate(Activity{ID: 8, Kind: "voice", Detail: "hola que tal amigos"})
	if called {
		t.Fatal("inactive translator must not fire OnTranslation")
	}
}

// A nil translator (feature not configured) must never fire either.
func TestMaybeTranslateNilTranslator(t *testing.T) {
	called := false
	m := New(Options{OnTranslation: func(a Activity, r translate.Result) { called = true }})
	m.maybeTranslate(Activity{ID: 9, Kind: "voice", Detail: "hola que tal"})
	if called {
		t.Fatal("nil translator must not fire OnTranslation")
	}
}
