package feeds

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"
)

// blockingConn never delivers a byte; it records read deadlines so tests
// can prove the feed bounds its reads.
type blockingConn struct {
	mu        sync.Mutex
	deadlines []time.Time
	closed    chan struct{}
	closeOnce sync.Once
}

func newBlockingConn() *blockingConn { return &blockingConn{closed: make(chan struct{})} }

func (c *blockingConn) Read(b []byte) (int, error) {
	<-c.closed
	return 0, net.ErrClosed
}
func (c *blockingConn) Write(b []byte) (int, error) { return len(b), nil }
func (c *blockingConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}
func (c *blockingConn) LocalAddr() net.Addr                { return nil }
func (c *blockingConn) RemoteAddr() net.Addr               { return nil }
func (c *blockingConn) SetDeadline(t time.Time) error      { return nil }
func (c *blockingConn) SetWriteDeadline(t time.Time) error { return nil }
func (c *blockingConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	c.deadlines = append(c.deadlines, t)
	c.mu.Unlock()
	return nil
}

func (c *blockingConn) readDeadlines() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.deadlines)
}

// The APRS-IS runner must put a read deadline on the socket before it
// reads the greeting; without it a silent server parks the feed forever.
func TestAPRSSetsReadDeadline(t *testing.T) {
	conn := newBlockingConn()
	a := &APRSIS{Dial: func(ctx context.Context, addr string) (net.Conn, error) { return conn, nil }}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx, func(Item) {}) }()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if conn.readDeadlines() > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if conn.readDeadlines() == 0 {
		t.Fatal("no read deadline was set on the APRS-IS socket")
	}
	cancel()
	<-done
}

func TestFeedBackoffWithJitter(t *testing.T) {
	base := 20 * time.Second
	for i := 0; i < 100; i++ {
		d := feedBackoffWithJitter(base)
		if d < base*3/4 || d >= base*5/4 {
			t.Fatalf("jittered backoff %s out of [%s, %s)", d, base*3/4, base*5/4)
		}
	}
}

// A run that survived past the healthy threshold resets the reconnect
// backoff; quick successive failures keep growing it.
func TestRunStreamBackoffReset(t *testing.T) {
	oldBase, oldThreshold := streamBackoffBase, healthyRunThreshold
	defer func() { streamBackoffBase, healthyRunThreshold = oldBase, oldThreshold }()
	streamBackoffBase = 50 * time.Millisecond
	healthyRunThreshold = 300 * time.Millisecond

	src := &scriptedSource{runs: []scriptedRun{
		{dur: 0, err: errFake},                      // fast fail: backoff grows
		{dur: 0, err: errFake},                      // fast fail: backoff grows
		{dur: 400 * time.Millisecond, err: errFake}, // healthy run: backoff resets
		{dur: 0, err: errFake},                      // fast fail again
	}}
	var delaysMu sync.Mutex
	var delays []time.Duration
	m := New(0, func(f string, a ...any) {
		for i, v := range a {
			if d, ok := v.(time.Duration); ok {
				_ = i
				delaysMu.Lock()
				delays = append(delays, d)
				delaysMu.Unlock()
			}
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { m.RunStream(ctx, src, 0); close(done) }()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		delaysMu.Lock()
		n := len(delays)
		delaysMu.Unlock()
		if n >= 4 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	delaysMu.Lock()
	defer delaysMu.Unlock()
	if len(delays) < 4 {
		t.Fatalf("only %d reconnect delays logged", len(delays))
	}
	// delays[0] ~= base, delays[1] ~= 2*base, delays[2] ~= base (reset
	// after the healthy run), delays[3] ~= 2*base.
	if delays[0] < 37*time.Millisecond || delays[0] > 63*time.Millisecond {
		t.Fatalf("first delay %s, want ~50ms", delays[0])
	}
	if delays[1] < 75*time.Millisecond || delays[1] > 125*time.Millisecond {
		t.Fatalf("second delay %s, want ~100ms (growth)", delays[1])
	}
	if delays[2] < 37*time.Millisecond || delays[2] > 63*time.Millisecond {
		t.Fatalf("post-healthy delay %s, want ~50ms (reset)", delays[2])
	}
	if delays[3] < 75*time.Millisecond || delays[3] > 125*time.Millisecond {
		t.Fatalf("fourth delay %s, want ~100ms (growth again)", delays[3])
	}
}

type errFakeT struct{}

func (errFakeT) Error() string { return "fake failure" }

var errFake error = errFakeT{}

type scriptedRun struct {
	dur time.Duration
	err error
}

type scriptedSource struct {
	mu    sync.Mutex
	calls int
	runs  []scriptedRun
}

func (s *scriptedSource) Name() string { return "scripted" }
func (s *scriptedSource) Kind() string { return "test" }
func (s *scriptedSource) Run(ctx context.Context, emit func(Item)) error {
	s.mu.Lock()
	idx := s.calls
	s.calls++
	s.mu.Unlock()
	run := s.runs[idx%len(s.runs)]
	if run.dur > 0 {
		t := time.NewTimer(run.dur)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
	return run.err
}
