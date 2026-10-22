package feeds

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"
)

func TestParseRBNSpot(t *testing.T) {
	it, ok := ParseRBNSpot("DX de WZ7I-#:     14074.0  4X1RF        FT8    17 dB              2001Z", time.Now())
	if !ok {
		t.Fatal("no item")
	}
	if it.Label != "4X1RF" {
		t.Fatalf("wrong call: %+v", it)
	}
	if it.Detail == "" {
		t.Fatalf("missing detail: %+v", it)
	}
	if _, ok := ParseRBNSpot("not a spot line", time.Now()); ok {
		t.Fatal("junk line accepted")
	}
}

func TestRBNRun(t *testing.T) {
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
		fmt.Fprint(conn, "Welcome to RBN. Please enter your call: ")
		buf := make([]byte, 64)
		conn.Read(buf) // the login
		fmt.Fprint(conn, "DX de WZ7I-#:     14074.0  4X1RF        CW    17 dB  25 WPM CQ      2001Z\r\n")
		time.Sleep(300 * time.Millisecond)
	}()
	src := &RBN{Server: ln.Addr().String()}
	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan Item, 2)
	done := make(chan error, 1)
	go func() { done <- src.Run(ctx, func(it Item) { got <- it }) }()
	var item Item
	select {
	case item = <-got:
	case <-time.After(2 * time.Second):
		cancel()
		<-done
		t.Fatal("timed out waiting for feed item")
	}
	cancel()
	<-done
	if item.Label != "4X1RF" {
		t.Fatalf("want 4X1RF spot, got %+v", item)
	}
}
