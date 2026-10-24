package web

import (
	"strings"
	"testing"
)

func TestRedactingWriterRedactsLikeRing(t *testing.T) {
	var sb strings.Builder
	w := NewRedactingWriter(&sb)
	in := []string{
		`feed: https://user:secret@feeds.example.com/live?lat=32.0853&lon=34.7818&key=abc123 connected`,
		`login attempt password=hunter2 from 127.0.0.1`,
		`Cookie: sdrstream_session=deadbeef; other=1`,
		`position lat=32.0853, lon=34.7818`,
		`decoded CQ CQ DE TEST grid KO87 145.8000 MHz`,
	}
	for _, l := range in {
		if _, err := w.Write([]byte(l + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	w.Flush()
	out := sb.String()
	for _, banned := range []string{"secret@", "abc123", "hunter2", "deadbeef", "32.0853", "34.7818", "user:secret"} {
		if strings.Contains(out, banned) {
			t.Fatalf("redaction leaked %q in:\n%s", banned, out)
		}
	}
	for _, kept := range []string{"feeds.example.com", "145.8000 MHz", "decoded CQ CQ DE TEST"} {
		if !strings.Contains(out, kept) {
			t.Fatalf("redaction ate legitimate content %q in:\n%s", kept, out)
		}
	}
}

func TestRedactingWriterBuffersPartialLines(t *testing.T) {
	var sb strings.Builder
	w := NewRedactingWriter(&sb)
	// A secret split across two writes must still be redacted whole.
	if _, err := w.Write([]byte("login pass")); err != nil {
		t.Fatal(err)
	}
	if sb.Len() != 0 {
		t.Fatalf("partial line leaked early: %q", sb.String())
	}
	if _, err := w.Write([]byte("word=hunter2 ok\n")); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sb.String(), "hunter2") {
		t.Fatalf("split secret leaked: %q", sb.String())
	}
	if !strings.Contains(sb.String(), "password=[redacted]") {
		t.Fatalf("expected redacted line, got %q", sb.String())
	}
}
