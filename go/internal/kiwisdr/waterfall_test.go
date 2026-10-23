package kiwisdr

import (
	"encoding/binary"
	"testing"
	"time"
)

// buildWFPacket assembles a synthetic uncompressed W/F packet the way a
// server sends it: 16 byte header, then one byte per bin.
func buildWFPacket(xbin uint32, zoom uint16, seq uint32, bins []byte) []byte {
	out := make([]byte, 16+len(bins))
	copy(out[0:4], []byte{'W', '/', 'F', 0})
	binary.LittleEndian.PutUint32(out[4:8], xbin)
	binary.LittleEndian.PutUint32(out[8:12], uint32(zoom))
	binary.LittleEndian.PutUint32(out[12:16], seq)
	copy(out[16:], bins)
	return out
}

func TestParseFrameUncompressed(t *testing.T) {
	// 0xCB = 203 as u1; the server encodes dB as (u1_t)(dB-1) over
	// [-200,0], so int8(203)+1 = -52 dB. 0x90 -> -111 dB.
	pkt := buildWFPacket(0, 0, 42, []byte{0xCB, 0x90, 0xFF})
	frame, ok := parseFrame(pkt, time.Now())
	if !ok {
		t.Fatal("uncompressed frame rejected")
	}
	if frame.XBin != 0 || frame.Zoom != 0 || frame.Seq != 42 {
		t.Errorf("header: xbin=%d zoom=%d seq=%d", frame.XBin, frame.Zoom, frame.Seq)
	}
	want := []float64{-52, -111, 0}
	for i, w := range want {
		if frame.Bins[i] != w {
			t.Errorf("bin %d: got %v want %v", i, frame.Bins[i], w)
		}
	}
}

func TestParseFrameRejectsCompressed(t *testing.T) {
	pkt := buildWFPacket(0, 0, 1, []byte{1, 2, 3})
	binary.LittleEndian.PutUint32(pkt[8:12], wfFlagCompression)
	if _, ok := parseFrame(pkt, time.Now()); ok {
		t.Fatal("compressed frame accepted; we negotiate wf_comp=0 and cannot decode this payload")
	}
}

func TestParseFrameRejectsGarbage(t *testing.T) {
	for _, pkt := range [][]byte{nil, {}, []byte("W/F"), []byte("SND\x00................"), make([]byte, 16)} {
		if _, ok := parseFrame(pkt, time.Now()); ok {
			t.Errorf("packet %q accepted", pkt)
		}
	}
}

func TestWaterfallSpectrumMapping(t *testing.T) {
	c := &WaterfallClient{}
	c.stateMu.Lock()
	c.fftSize = 1024
	c.centerHz = 15e6
	c.bandwidthHz = 30e6
	c.latest = WaterfallFrame{XBin: 0, Zoom: 0, Bins: make([]float64, 1024), ReceivedAt: time.Now()}
	c.haveLatest = true
	c.stateMu.Unlock()
	bins, centre, span, ok := c.Spectrum()
	if !ok {
		t.Fatal("no spectrum")
	}
	if len(bins) != 1024 {
		t.Fatalf("bins %d", len(bins))
	}
	// zoom 0: 1024 bins over 30 MHz starting at 0 Hz.
	if span != 30e6 {
		t.Errorf("span %v, want 30e6", span)
	}
	if centre != 15e6 {
		t.Errorf("centre %v, want 15e6", centre)
	}
}

func TestWaterfallSpectrumZoomedMapping(t *testing.T) {
	c := &WaterfallClient{}
	c.stateMu.Lock()
	c.fftSize = 1024
	c.centerHz = 15e6
	c.bandwidthHz = 30e6
	// zoom 1: one bin is 30e6/2048 Hz; xbin 512 starts the window at
	// 512*binHz = 7.5 MHz.
	c.latest = WaterfallFrame{XBin: 512, Zoom: 1, Bins: make([]float64, 1024), ReceivedAt: time.Now()}
	c.haveLatest = true
	c.stateMu.Unlock()
	_, centre, span, ok := c.Spectrum()
	if !ok {
		t.Fatal("no spectrum")
	}
	binHz := 30e6 / 2048
	if span != 1024*binHz {
		t.Errorf("span %v, want %v", span, 1024*binHz)
	}
	wantCentre := 512*binHz + 1024*binHz/2
	if centre != wantCentre {
		t.Errorf("centre %v, want %v", centre, wantCentre)
	}
}

func TestWaterfallSpectrumFreshnessGate(t *testing.T) {
	c := &WaterfallClient{}
	if _, _, _, ok := c.Spectrum(); ok {
		t.Fatal("spectrum without any frame")
	}
	c.stateMu.Lock()
	c.fftSize = 1024
	c.centerHz = 15e6
	c.bandwidthHz = 30e6
	c.latest = WaterfallFrame{Bins: make([]float64, 1024), ReceivedAt: time.Now().Add(-2 * time.Minute)}
	c.haveLatest = true
	c.stateMu.Unlock()
	if _, _, _, ok := c.Spectrum(); ok {
		t.Fatal("stale frame served")
	}
	c.stateMu.Lock()
	c.latest.ReceivedAt = time.Now().Add(-20 * time.Second)
	c.dead = true
	c.stateMu.Unlock()
	if _, _, _, ok := c.Spectrum(); ok {
		t.Fatal("frozen frame from a dead connection served")
	}
}

func TestWaterfallHandleText(t *testing.T) {
	c := &WaterfallClient{}
	c.handleText("MSG wf_fft_size=1024 wf_fps=23 zoom_max=14")
	c.handleText("MSG center_freq=15000000")
	c.handleText("MSG bandwidth=30000000")
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	if c.fftSize != 1024 || c.centerHz != 15e6 || c.bandwidthHz != 30e6 {
		t.Errorf("geometry: fft=%d centre=%v bw=%v", c.fftSize, c.centerHz, c.bandwidthHz)
	}
}
