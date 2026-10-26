package iqsrc

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

// fakeServer is an in-process rtl_tcp server: serves the header, records
// command bytes verbatim, then streams a known I/Q pattern until told to stop.
type fakeServer struct {
	ln       net.Listener
	cmdBytes int // how many command bytes to capture before streaming (0 = none)
	commands []byte
	cmdsDone chan struct{}
	stream   []byte
	stop     chan struct{}
}

func newFakeServer(t *testing.T, stream []byte, cmdBytes int) *fakeServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeServer{ln: ln, cmdBytes: cmdBytes, stream: stream, stop: make(chan struct{}), cmdsDone: make(chan struct{})}
	go f.serve()
	return f
}

func (f *fakeServer) serve() {
	conn, err := f.ln.Accept()
	if err != nil {
		return
	}
	defer conn.Close()
	var hdr [12]byte
	copy(hdr[0:4], "RTL0")
	binary.BigEndian.PutUint32(hdr[4:8], TunerR820T)
	binary.BigEndian.PutUint32(hdr[8:12], 29)
	conn.Write(hdr[:])
	// Capture the expected command bytes (if any), then stream.
	if f.cmdBytes > 0 {
		buf := make([]byte, f.cmdBytes)
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		io.ReadFull(conn, buf)
		conn.SetReadDeadline(time.Time{})
		f.commands = append(f.commands, buf...)
	}
	close(f.cmdsDone)
	for {
		select {
		case <-f.stop:
			return
		default:
		}
		conn.SetWriteDeadline(time.Now().Add(200 * time.Millisecond))
		if _, err := conn.Write(f.stream); err != nil {
			return
		}
	}
}

func (f *fakeServer) close() { close(f.stop); f.ln.Close() }

func TestDialHeaderAndCommands(t *testing.T) {
	f := newFakeServer(t, make([]byte, 4096), 25)
	defer f.close()
	_, port, _ := net.SplitHostPort(f.ln.Addr().String())
	c, err := Dial(context.Background(), Config{
		Addr: "127.0.0.1:" + port, Frequency: 144800000, SampleRate: 240000,
		ManualGain: true, GainTenthsDB: 250, AGC: true,
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()
	if c.TunerType() != TunerR820T || c.TunerGainCount() != 29 {
		t.Fatalf("header: type=%d gains=%d", c.TunerType(), c.TunerGainCount())
	}
	want := []byte{
		cmdSetGainMode, 0, 0, 0, 1,
		cmdSetAGC, 0, 0, 0, 1,
		cmdSetGain, 0, 0, 0, 250,
		cmdSetSampleRate, 0, 3, 0xA9, 0x80, // 240000
		cmdSetFreq, 8, 0xA1, 0x79, 0x00, // 144800000
	}
	<-f.cmdsDone
	if len(f.commands) != len(want) {
		t.Fatalf("commands len %d want %d: %x", len(f.commands), len(want), f.commands)
	}
	for i := range want {
		if f.commands[i] != want[i] {
			t.Fatalf("command byte %d: got %x want %x (all %x)", i, f.commands[i], want[i], f.commands)
		}
	}
}

func TestBadMagic(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, _ := ln.Accept()
		conn.Write([]byte("NOPE00000000"))
		conn.Close()
	}()
	if _, err := Dial(context.Background(), Config{Addr: ln.Addr().String()}); err == nil {
		t.Fatal("Dial against non-rtl_tcp server succeeded")
	}
}

func TestReadSamplesConversion(t *testing.T) {
	// Known pattern: extremes and midpoints.
	stream := []byte{0, 0, 255, 255, 127, 128, 200, 55}
	f := newFakeServer(t, stream, 0)
	defer f.close()
	c, err := Dial(context.Background(), Config{Addr: f.ln.Addr().String(), Frequency: 1, SampleRate: 1})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()
	buf := make([]complex64, 4)
	n, err := c.ReadSamples(buf)
	if err != nil && err != io.ErrUnexpectedEOF {
		t.Fatalf("ReadSamples: %v", err)
	}
	if n != 4 {
		t.Fatalf("got %d samples want 4", n)
	}
	check := func(i int, wr, wi float32) {
		r, im := real(buf[i]), imag(buf[i])
		if d := r - wr; d > 0.001 || d < -0.001 {
			t.Fatalf("sample %d real %f want %f", i, r, wr)
		}
		if d := im - wi; d > 0.001 || d < -0.001 {
			t.Fatalf("sample %d imag %f want %f", i, im, wi)
		}
	}
	check(0, -127.5/128, -127.5/128)
	check(1, 127.5/128, 127.5/128)
	check(2, -0.5/128, 0.5/128)
	check(3, 72.5/128, -72.5/128)
}

func TestSetFrequencyAndClose(t *testing.T) {
	f := newFakeServer(t, make([]byte, 64), 0)
	defer f.close()
	c, err := Dial(context.Background(), Config{Addr: f.ln.Addr().String(), Frequency: 1, SampleRate: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SetFrequency(435000000); err != nil {
		t.Fatalf("SetFrequency: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := c.SetFrequency(1); err == nil {
		t.Fatal("SetFrequency after Close succeeded")
	}
}
