package monitor

import (
	"context"
	"encoding/binary"
	"io"
	"math"
	"net"
	"strings"
	"testing"
	"time"
)

// fakeSpyServer streams a continuous uint8-IQ FM signal (carrier at channel
// center, 1 kHz modulating tone, 3 kHz deviation) so a Listen through the
// real iqsrc client + iqdemod chain must recover the tone.
type fakeSpyServer struct {
	ln   net.Listener
	stop chan struct{}
}

func newFakeSpyServer(t *testing.T) *fakeSpyServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSpyServer{ln: ln, stop: make(chan struct{})}
	go f.serve()
	return f
}

func (f *fakeSpyServer) serve() {
	conn, err := f.ln.Accept()
	if err != nil {
		return
	}
	defer conn.Close()
	var hdr [8]byte
	// HELLO
	if _, err := io.ReadFull(conn, hdr[:]); err != nil {
		return
	}
	hello := make([]byte, binary.LittleEndian.Uint32(hdr[4:8]))
	io.ReadFull(conn, hello)
	put := func(mtype uint32, body []byte) {
		m := make([]byte, 20+len(body))
		binary.LittleEndian.PutUint32(m[0:4], 0)
		binary.LittleEndian.PutUint32(m[4:8], mtype)
		binary.LittleEndian.PutUint32(m[16:20], uint32(len(body)))
		copy(m[20:], body)
		conn.Write(m)
	}
	di := make([]byte, 52)
	// MaxSampleRate 7.68M, DecimationStageCount 5, MinimumIQDecimation 0,
	// MaxGainIndex 21; rest zero-ish.
	for i, v := range []uint32{3, 1, 7680000, 6000000, 5, 22, 21, 1000, 1800000000, 12, 0, 0} {
		binary.LittleEndian.PutUint32(di[4*i:], v)
	}
	put(0, di)
	cs := make([]byte, 40)
	binary.LittleEndian.PutUint32(cs[0:4], 1)
	binary.LittleEndian.PutUint32(cs[12:16], 144800000)
	put(1, cs)
	// Read settings until STREAMING_ENABLED=1.
	for {
		if _, err := io.ReadFull(conn, hdr[:]); err != nil {
			return
		}
		body := make([]byte, binary.LittleEndian.Uint32(hdr[4:8]))
		io.ReadFull(conn, body)
		if binary.LittleEndian.Uint32(hdr[0:4]) == 2 && len(body) == 8 &&
			binary.LittleEndian.Uint32(body[0:4]) == 1 && binary.LittleEndian.Uint32(body[4:8]) == 1 {
			break
		}
	}
	// Stream FM tone forever (blocks of 16384 samples).
	const rate = 240000 // negotiated: 7.68M / 2^5
	block := make([]byte, 2*16384)
	phase := 0.0
	idx := 0
	for {
		select {
		case <-f.stop:
			return
		default:
		}
		for i := 0; i < 16384; i++ {
			t := float64(idx) / rate
			inst := 3000 * math.Sin(2*math.Pi*1000*t)
			phase += 2 * math.Pi * inst / rate
			re := byte(math.Cos(phase)*100 + 128)
			im := byte(math.Sin(phase)*100 + 128)
			block[2*i], block[2*i+1] = re, im
			idx++
		}
		put(100, block)
	}
}

func (f *fakeSpyServer) close() { close(f.stop); f.ln.Close() }

func goertzelPow(x []float64, fs, freq float64) float64 {
	w := 2 * math.Pi * freq / fs
	var s, c float64
	for i, v := range x {
		s += v * math.Sin(w*float64(i))
		c += v * math.Cos(w*float64(i))
	}
	return 2 * math.Sqrt(s*s+c*c) / float64(len(x))
}

func TestSpySourceListenRecoversFMTone(t *testing.T) {
	f := newFakeSpyServer(t)
	defer f.close()
	src, err := SpyServerSource(context.Background(), f.ln.Addr().String(), 144800000, 250000, 0)
	if err != nil {
		t.Fatalf("SpyServerSource: %v", err)
	}
	defer src.Close()
	audio, rate, err := src.Listen(context.Background(), 144800000, "nbfm", 500*time.Millisecond)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	if rate != 12000 {
		t.Fatalf("rate %d", rate)
	}
	if len(audio) < 5500 {
		t.Fatalf("audio len %d", len(audio))
	}
	skip := audio[1200:]
	p1k := goertzelPow(skip, float64(rate), 1000)
	p2k := goertzelPow(skip, float64(rate), 2000)
	if p1k < 5*p2k {
		t.Fatalf("1kHz %.4f not dominant over 2kHz %.4f", p1k, p2k)
	}
	if _, _, _, ok := src.Spectrum(); ok {
		t.Fatal("spySource must report no spectrum on IQ-only stream")
	}
}

func TestSpyModeMap(t *testing.T) {
	for _, m := range []string{"usb", "lsb", "am", "nbfm", "fm", "cw", " USB "} {
		if _, err := spyMode(m); err != nil {
			t.Fatalf("mode %q: %v", m, err)
		}
	}
	if _, err := spyMode("wfm"); err == nil {
		t.Fatal("wfm must be rejected (not implemented)")
	}
}

// TestSourceForLocalSpyserver wires the "spyserver" kind through the same
// factory the UI's monitor-start hook uses.
func TestSourceForLocalSpyserver(t *testing.T) {
	f := newFakeSpyServer(t)
	defer f.close()
	src, err := SourceForLocal(context.Background(), "spyserver", f.ln.Addr().String(), "")
	if err != nil {
		t.Fatalf("SourceForLocal spyserver: %v", err)
	}
	defer src.Close()
	if _, _, _, ok := src.Spectrum(); ok {
		t.Fatal("spyserver source must report no spectrum (IQ-only stream)")
	}
}

// TestSourceForSpyserverRejectsLoopback keeps the SSRF guard on the
// directory-sourced path: even a reachable loopback server must be refused.
func TestSourceForSpyserverRejectsLoopback(t *testing.T) {
	f := newFakeSpyServer(t)
	defer f.close()
	_, err := SourceFor(context.Background(), "spyserver", f.ln.Addr().String(), "")
	if err == nil || !strings.Contains(err.Error(), "non-public") {
		t.Fatalf("guarded SourceFor did not reject loopback spyserver: %v", err)
	}
}
