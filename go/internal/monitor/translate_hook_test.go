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
	act := Activity{ID: 7, Kind: "voice", Mode: "speech", Detail: "hola que tal amigos del mar"}
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
	m.maybeTranslate(Activity{ID: 8, Kind: "voice", Mode: "speech", Detail: "hola que tal amigos"})
	if called {
		t.Fatal("inactive translator must not fire OnTranslation")
	}
}

// A nil translator (feature not configured) must never fire either.
func TestMaybeTranslateNilTranslator(t *testing.T) {
	called := false
	m := New(Options{OnTranslation: func(a Activity, r translate.Result) { called = true }})
	m.maybeTranslate(Activity{ID: 9, Kind: "voice", Mode: "speech", Detail: "hola que tal"})
	if called {
		t.Fatal("nil translator must not fire OnTranslation")
	}
}

// No metadata is concatenated with the transcript passed to the provider.
type captureTranslator struct{ seen chan string }

func (f captureTranslator) Name() string  { return "fake" }
func (f captureTranslator) Model() string { return "fixture" }
func (f captureTranslator) Translate(_ context.Context, text string) (translate.Output, error) {
	f.seen <- text
	return translate.Output{Text: "Strong wind tomorrow for all ships in the gulf"}, nil
}
func TestMaybeTranslateMinimumDisclosure(t *testing.T) {
	seen := make(chan string, 1)
	done := make(chan struct{}, 1)
	tr := translate.New(translate.Config{Enabled: true, ConsentedProvider: "fake"}, captureTranslator{seen})
	m := New(Options{Translator: tr, OnTranslation: func(Activity, translate.Result) { done <- struct{}{} }})
	text := "Attention tous les navires, vent fort prévu demain matin dans le golfe"
	m.maybeTranslate(Activity{Kind: "voice", Mode: "speech", Detail: text, Source: "private-receiver.example", Label: "private label", FrequencyHz: 1234567, AudioPath: "/private/audio.wav"})
	select {
	case got := <-seen:
		if got != text {
			t.Fatalf("provider received %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("provider not called")
	}
	<-done
}
func TestMaybeTranslateRejectsDiagnosticAndIdentifierDisclosure(t *testing.T) {
	seen := make(chan string, 8)
	done := make(chan translate.Result, 8)
	tr := translate.New(translate.Config{Enabled: true, ConsentedProvider: "fake"}, captureTranslator{seen})
	m := New(Options{Translator: tr, OnTranslation: func(_ Activity, r translate.Result) { done <- r }})
	for _, a := range []Activity{
		{Kind: "voice", Mode: "am", Detail: "Attention tous les navires, diagnostic sans transcription"},
		{Kind: "data", Mode: "speech", Detail: "Attention tous les navires, diagnostic sans transcription"},
		{Kind: "voice", Mode: "speech", Detail: ""},
	} {
		m.maybeTranslate(a)
	}
	// Identifiers in an actual transcript are still filtered downstream.
	m.maybeTranslate(Activity{Kind: "voice", Mode: "speech", Detail: "Attention tous les navires, contactez moi a personne@example.com"})
	select {
	case r := <-done:
		if r.Status != translate.StatusIneligible {
			t.Fatalf("%+v", r)
		}
	case <-time.After(time.Second):
		t.Fatal("missing ineligible result")
	}
	select {
	case got := <-seen:
		t.Fatalf("unexpected disclosure %q", got)
	default:
	}
	select {
	case r := <-done:
		t.Fatalf("diagnostic generated result %+v", r)
	default:
	}
}
