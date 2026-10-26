package iqsrc

// Handshake audit matrix, written after comparing DialSpy against the SDR++
// reference client (spyserver_client.cpp / spyserver_main.cpp): the reference
// waits 3s for DEVICE_INFO ONLY and never requires CLIENT_SYNC. These tests
// pin the divergences and the parser behaviors that a live server can
// exercise, using scripted in-process servers (no external contacts).

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// scriptSpy is a minimal programmable server: it reads the HELLO command,
// runs script(conn), and is done. Fragments writes when asked.
type scriptSpy struct {
	ln   net.Listener
	addr string
}

func newScriptSpy(t *testing.T, script func(net.Conn)) *scriptSpy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &scriptSpy{ln: ln, addr: ln.Addr().String()}
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var hdr [8]byte
		if _, err := io.ReadFull(conn, hdr[:]); err != nil {
			return
		}
		body := make([]byte, binary.LittleEndian.Uint32(hdr[4:8]))
		io.ReadFull(conn, body)
		script(conn)
		// Keep the connection open briefly so the client sees an orderly
		// close, not a reset, if it is still reading.
		time.Sleep(100 * time.Millisecond)
	}()
	return s
}

func deviceInfoMsg() []byte {
	di := make([]byte, 48)
	for i, v := range []uint32{3, 0xBEEF, 10000000, 8000000, 5, 22, 21, 1000, 1800000000, 12, 2, 0} {
		binary.LittleEndian.PutUint32(di[4*i:], v)
	}
	m := make([]byte, 20+len(di))
	putMsg(m[:20], spyMsgDeviceInfo, 0, 0, di)
	copy(m[20:], di)
	return m
}

func clientSyncMsg() []byte {
	cs := make([]byte, 40)
	for i, v := range []uint32{1, 10, 145000000, 144800000, 0, 0, 0, 0, 0} {
		binary.LittleEndian.PutUint32(cs[4*i:], v)
	}
	m := make([]byte, 20+len(cs))
	putMsg(m[:20], spyMsgClientSync, 0, 1, cs)
	copy(m[20:], cs)
	return m
}

// writeFragments sends b in small chunks with tiny delays to force partial
// TCP reads on the client side.
func writeFragments(conn net.Conn, b []byte) {
	for i := 0; i < len(b); i += 7 {
		end := i + 7
		if end > len(b) {
			end = len(b)
		}
		conn.Write(b[i:end])
		time.Sleep(2 * time.Millisecond)
	}
}

func dialCtx(t *testing.T, s *scriptSpy, freq uint32) (*SpyClient, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	return DialSpy(ctx, SpyConfig{Addr: s.addr, Frequency: freq, AllowPrivate: true})
}

// Reference parity: a server that answers HELLO with DEVICE_INFO but never
// sends CLIENT_SYNC must still connect (reference waits for DEVICE_INFO
// only). Sync facts become "not reported", never zero-valued.
func TestSpyHandshakeDeviceInfoOnlyProceeds(t *testing.T) {
	s := newScriptSpy(t, func(conn net.Conn) {
		conn.Write(deviceInfoMsg())
		time.Sleep(3 * time.Second) // stay connected; never send CLIENT_SYNC
	})
	defer s.ln.Close()
	start := time.Now()
	c, err := dialCtx(t, s, 144800000)
	if err != nil {
		t.Fatalf("DialSpy with DEVICE_INFO only: %v (reference client proceeds in <=3s)", err)
	}
	defer c.Close()
	if c.HaveSync {
		t.Fatal("HaveSync must be false when no CLIENT_SYNC arrived")
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("took %v; should proceed on DEVICE_INFO + short grace, not the full handshake deadline", elapsed)
	}
}

// Without CLIENT_SYNC and without a configured frequency there is no honest
// center to tune: fail fast with a clear reason instead of guessing.
func TestSpyHandshakeNoFrequencyWithoutSyncFails(t *testing.T) {
	s := newScriptSpy(t, func(conn net.Conn) {
		conn.Write(deviceInfoMsg())
		time.Sleep(3 * time.Second)
	})
	defer s.ln.Close()
	_, err := dialCtx(t, s, 0)
	if err == nil {
		t.Fatal("expected error: no configured frequency and no CLIENT_SYNC to inherit one from")
	}
}

// Fragmented delivery must not corrupt framing (io.ReadFull semantics).
func TestSpyHandshakePartialReads(t *testing.T) {
	s := newScriptSpy(t, func(conn net.Conn) {
		writeFragments(conn, deviceInfoMsg())
		writeFragments(conn, clientSyncMsg())
		time.Sleep(500 * time.Millisecond)
	})
	defer s.ln.Close()
	c, err := dialCtx(t, s, 0)
	if err != nil {
		t.Fatalf("fragmented handshake: %v", err)
	}
	defer c.Close()
	if !c.HaveSync || c.Sync.IQCenterFrequency != 144800000 {
		t.Fatalf("bad sync parse: have=%v %+v", c.HaveSync, c.Sync)
	}
}

// Unknown/interleaved messages before DEVICE_INFO must be skipped.
func TestSpyHandshakeUnknownMessagesSkipped(t *testing.T) {
	s := newScriptSpy(t, func(conn net.Conn) {
		var unk [20]byte
		putMsg(unk[:], 0xAAAA, 0, 9, nil) // unknown type, empty body
		conn.Write(unk[:])
		pong := make([]byte, 20)
		putMsg(pong[:20], spyMsgPong, 0, 9, nil)
		conn.Write(pong)
		conn.Write(deviceInfoMsg())
		conn.Write(clientSyncMsg())
		time.Sleep(300 * time.Millisecond)
	})
	defer s.ln.Close()
	c, err := dialCtx(t, s, 0)
	if err != nil {
		t.Fatalf("handshake with unknown interleave: %v", err)
	}
	defer c.Close()
	if !c.HaveSync {
		t.Fatal("expected CLIENT_SYNC parsed after unknown messages")
	}
}

// A server that accepts TCP and says nothing must fail with an error that
// SAYS nothing arrived (observability for the live batch harness).
func TestSpyHandshakeSilentServerReportsNothingReceived(t *testing.T) {
	s := newScriptSpy(t, func(conn net.Conn) { time.Sleep(7 * time.Second) })
	defer s.ln.Close()
	start := time.Now()
	_, err := dialCtx(t, s, 144800000)
	if err == nil {
		t.Fatal("silent server must fail")
	}
	if elapsed := time.Since(start); elapsed > 7*time.Second {
		t.Fatalf("handshake deadline not enforced: %v", elapsed)
	}
	// The error must make partial state auditable, not just "i/o timeout".
	if got := err.Error(); !containsAny(got, "no response", "0 messages", "nothing received") {
		t.Fatalf("error must state nothing was received, got: %v", err)
	}
}

// Oversize announced body must be rejected before allocation abuse.
func TestSpyHandshakeOversizeMessageRejected(t *testing.T) {
	s := newScriptSpy(t, func(conn net.Conn) {
		var m [20]byte
		putMsg(m[:20], spyMsgDeviceInfo, 0, 0, nil)
		binary.LittleEndian.PutUint32(m[16:20], 2<<20) // 2MB > 1MB cap
		conn.Write(m[:])
		time.Sleep(time.Second)
	})
	defer s.ln.Close()
	_, err := dialCtx(t, s, 144800000)
	if err == nil {
		t.Fatal("oversize message must fail")
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
