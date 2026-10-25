package feeds

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"
)

func TestParseDXLine(t *testing.T) {
	spotter, dxcall, freq, comment, ok := ParseDXLine("DX de W3LPL:     14074.0  K1ABC       FT8 -11 dB                0658Z\r\n")
	if !ok {
		t.Fatal("no parse")
	}
	if spotter != "W3LPL" || dxcall != "K1ABC" || freq != 14074.0 || comment != "FT8 -11 dB" {
		t.Fatalf("bad parse: %q %q %v %q", spotter, dxcall, freq, comment)
	}
	for _, junk := range []string{
		"Welcome to NC7J, running AR-Cluster 6.6.186",
		"Please enter your call:",
		"To ALL de K1TTT de WC2L-10 de W3LPL: hello there",
		"",
	} {
		if _, _, _, _, ok := ParseDXLine(junk); ok {
			t.Fatalf("junk accepted: %q", junk)
		}
	}
}

func TestDXClusterRun(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		fmt.Fprint(conn, "Welcome to the cluster. Please enter your call:\r\n")
		buf := make([]byte, 64)
		conn.Read(buf) // the login
		fmt.Fprint(conn, "DX de W3LPL:     14074.0  K1ABC       FT8 -11 dB                0658Z\r\n")
		time.Sleep(300 * time.Millisecond)
	}()
	src := &DXCluster{Server: ln.Addr().String()}
	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan Item, 2)
	ready := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() { done <- src.RunReady(ctx, func(it Item) { got <- it }, func() { ready <- struct{}{} }) }()
	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		cancel()
		<-done
		t.Fatal("never logged in")
	}
	var item Item
	select {
	case item = <-got:
	case <-time.After(2 * time.Second):
		cancel()
		<-done
		t.Fatal("timed out waiting for spot")
	}
	cancel()
	<-done
	if item.Source != "dxcluster" || item.Kind != "dx" {
		t.Fatalf("bad source/kind: %+v", item)
	}
	if item.Label != "K1ABC 14.074 MHz" {
		t.Fatalf("bad label: %+v", item)
	}
}

// A healthy cluster that stays spotless must NOT churn reconnects: the
// read deadline refreshes on any line, and only a fully silent peer is
// treated as dead (device evidence 2026-10-25: ~90 s i/o-timeout loop).
func TestDXClusterIdleDeadlineRefreshesOnAnyLine(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		fmt.Fprint(conn, "Welcome. Please enter your call:\r\n")
		buf := make([]byte, 64)
		conn.Read(buf)
		// Banners/talk but no spots, at a cadence below the idle timeout,
		// for LONGER than the pre-fix 90 s-style deadline would allow.
		for i := 0; i < 6; i++ {
			fmt.Fprint(conn, "Cluster node chatter line\r\n")
			time.Sleep(60 * time.Millisecond)
		}
		time.Sleep(500 * time.Millisecond) // then go fully silent: must drop
	}()
	src := &DXCluster{Server: ln.Addr().String(), IdleTimeout: 150 * time.Millisecond}
	done := make(chan error, 1)
	go func() { done <- src.Run(context.Background(), func(Item) {}) }()
	// During chatter the connection must stay up (pre-fix: spotless lines
	// did not refresh the deadline, so it dropped at ~150 ms).
	select {
	case err := <-done:
		t.Fatalf("dropped during chatter at ~150ms? err=%v (pre-fix behavior)", err)
	case <-time.After(300 * time.Millisecond):
	}
	// Once the peer goes fully silent it must be dropped within the idle timeout.
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("silent peer must eventually drop the connection")
		}
	case <-time.After(600 * time.Millisecond):
		t.Fatal("silent peer never dropped")
	}
}
