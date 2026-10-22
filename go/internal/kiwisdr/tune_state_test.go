package kiwisdr

import (
	"sync"
	"testing"
)

// Review M7: the reader goroutine writes rate/info while any listener can
// snapshot them, so both accessors must take the state lock. Go's race
// detector is the verification: without the mutex this test reports a data
// race on -race (it did, before the fix).
func TestInfoAndRateAccessorsAreLocked(t *testing.T) {
	c, err := New("localhost:8073", DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	done := make(chan struct{})
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			c.handleText("MSG sample_rate=20250 bandwidth=13.2 rx_chans=8 'load_cfg'")
		}
		close(done)
	}()
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
				_ = c.InfoSnapshot()
				_ = c.SampleRate()
			}
		}
	}()
	wg.Wait()
}

// handleText is the only path that advances the session state that
// SampleRate/InfoSnapshot expose under stateMu. A drain that swallowed
// MSG frames (the first attempt at review M4 on this client) would
// silently lose sample_rate and the version; these assert the routing works.
func TestTuneDrainKeepsTextFrames(t *testing.T) {
	c, err := New("localhost:8073", DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	c.handleText("MSG sample_rate=20250")
	if got := c.SampleRate(); got != 20250 {
		t.Fatalf("SampleRate = %d, want 20250 (MSG frame routed to handleText)", got)
	}
	// The version pair lives inside one frame; handleText re-scans for it
	// explicitly, so a dropped MSG frame loses the version entirely.
	c.handleText("MSG version_maj=1 version_min=1024 load_cfg")
	if got := c.InfoSnapshot().Version; got != "1.1024" {
		t.Fatalf("Info.Version = %q, want 1.1024 (MSG frame routed to handleText)", got)
	}
}
