package feeds

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

func TestParseAPRSPosition(t *testing.T) {
	it, ok := ParseAPRSLine("5B4ANU-10>APLRG1,WIDE1-1,qAR,5B4APV-10:!3504.50N/03325.50E>Aglandjia digi", time.Now())
	if !ok {
		t.Fatal("no item")
	}
	if !it.HasPos {
		t.Fatalf("position not parsed: %+v", it)
	}
	if it.Lat < 35 || it.Lat > 35.2 || it.Lon < 33.3 || it.Lon > 33.6 {
		t.Fatalf("wrong coordinates: %v %v", it.Lat, it.Lon)
	}
	if it.Label != "5B4ANU-10" {
		t.Fatalf("wrong label %q", it.Label)
	}
}

func TestParseAPRSStatusAndTelemetry(t *testing.T) {
	it, ok := ParseAPRSLine("4X1RF>APRS,TCPIP*:>Testing the status line", time.Now())
	if !ok || it.HasPos || !strings.HasPrefix(it.Detail, "status: Testing") {
		t.Fatalf("status packet mishandled: %+v", it)
	}
	it, _ = ParseAPRSLine("4X1RF>APRS,TCPIP*:T#123,456,789", time.Now())
	if it.Detail != "packet via APRS-IS" {
		t.Fatalf("telemetry should fall back: %+v", it)
	}
}

func TestAPRSISRunReadOnly(t *testing.T) {
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
		fmt.Fprint(conn, "# aprsc test 2.1\n")
		r := bufio.NewReader(conn)
		login, _ := r.ReadString('\n')
		if !strings.Contains(login, "user NOCALL pass -1") || !strings.Contains(login, "filter r/32.0800/34.7800/400") {
			t.Errorf("bad login line: %q", login)
		}
		fmt.Fprint(conn, "# logresp NOCALL unverified, server TEST\n")
		fmt.Fprint(conn, "5B4ANU-10>APLRG1,WIDE1-1,qAR,5B4APV-10:!3504.50N/03325.50E>Aglandjia digi\r\n")
		fmt.Fprint(conn, "4X1RF>APRS,TCPIP*:>home QTH\r\n")
		time.Sleep(200 * time.Millisecond)
	}()
	src := &APRSIS{Server: ln.Addr().String(), Lat: 32.08, Lon: 34.78, Radius: 400}
	ctx, cancel := context.WithCancel(context.Background())
	var got []Item
	done := make(chan error, 1)
	go func() { done <- src.Run(ctx, func(it Item) { got = append(got, it) }) }()
	time.Sleep(500 * time.Millisecond)
	cancel()
	<-done
	if len(got) != 2 {
		t.Fatalf("want 2 packets, got %d (%+v)", len(got), got)
	}
	if !got[0].HasPos {
		t.Fatalf("first packet should carry a position: %+v", got[0])
	}
}
