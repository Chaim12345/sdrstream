package web

import (
	"strings"
	"testing"
)

func TestLogRingRedaction(t *testing.T) {
	ring := NewLogRing(10)
	in := []string{
		`feed: https://user:secret@feeds.example.com/live?lat=32.0853&lon=34.7818&key=abc123 connected`,
		`login attempt password=hunter2 from 127.0.0.1`,
		`Cookie: sdrstream_session=deadbeef; other=1`,
		`position lat=32.0853, lon=34.7818`,
		`decoded CQ CQ DE TEST grid KO87 145.8000 MHz`,
	}
	for _, l := range in {
		ring.Write([]byte(l + "\n"))
	}
	out := strings.Join(ring.Tail(10), "\n")
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
