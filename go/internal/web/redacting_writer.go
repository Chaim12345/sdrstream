package web

import (
	"io"
	"strings"
	"sync"
)

// RedactingWriter applies redactLogLine to every complete line before
// forwarding it downstream. The engine's standard log goes through it on
// Android, where stdout is captured verbatim into a shareable engine.log:
// without it that file could carry feed credentials, cookies, URL
// queries/userinfo and keyed coordinates that the log ring already strips
// (logring_redaction_test.go). Line-buffered so a redaction regex never
// sees half a secret; a partial line longer than 4KB is flushed redacted
// so a writer stall cannot buffer unbounded output.
type RedactingWriter struct {
	mu  sync.Mutex
	w   io.Writer
	buf []byte
}

// NewRedactingWriter wraps w with line-wise log redaction.
func NewRedactingWriter(w io.Writer) *RedactingWriter {
	return &RedactingWriter{w: w}
}

func (rw *RedactingWriter) Write(p []byte) (int, error) {
	rw.mu.Lock()
	defer rw.mu.Unlock()
	rw.buf = append(rw.buf, p...)
	for {
		i := strings.IndexByte(string(rw.buf), '\n')
		if i < 0 {
			break
		}
		line := string(rw.buf[:i])
		rw.buf = rw.buf[i+1:]
		if _, err := io.WriteString(rw.w, redactLogLine(line)+"\n"); err != nil {
			return 0, err
		}
	}
	if len(rw.buf) > 4096 {
		if _, err := io.WriteString(rw.w, redactLogLine(string(rw.buf))); err != nil {
			return 0, err
		}
		rw.buf = rw.buf[:0]
	}
	return len(p), nil
}

// Flush writes any buffered partial line, redacted.
func (rw *RedactingWriter) Flush() {
	rw.mu.Lock()
	defer rw.mu.Unlock()
	if len(rw.buf) > 0 {
		_, _ = io.WriteString(rw.w, redactLogLine(string(rw.buf)))
		rw.buf = rw.buf[:0]
	}
}
