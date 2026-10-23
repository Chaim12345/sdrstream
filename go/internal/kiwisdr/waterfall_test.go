package kiwisdr

import (
	"math"
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
	// zoom 1: one bin is 30e6/2048 Hz. x_bin counts max-zoom bins
	// (hzPerMaxBin = 30e6/(1024<<14)), so an x_bin of 512<<13 starts
	// the window at 7.5 MHz.
	c.latest = WaterfallFrame{XBin: 512 << 13, Zoom: 1, Bins: make([]float64, 1024), ReceivedAt: time.Now()}
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
	hzPerMaxBin := 30e6 / float64(1024<<14)
	wantCentre := float64(512<<13)*hzPerMaxBin + 1024*binHz/2
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

func TestZoomWindowMath(t *testing.T) {
	// 30 MHz band, 1024-bin frames: zoom 5 gives ~915 Hz bins, zoom 4
	// still ~1.83 kHz, so a 1.5 kHz target lands on zoom 5 centred on
	// the signal.
	zoom, start := zoomWindow(7.2e6, 1500, 1024, 30e6, 15e6, 14)
	if zoom != 5 {
		t.Fatalf("zoom %d, want 5", zoom)
	}
	// start counts max-zoom bins: hzPerMaxBin = 30e6 / (1024 << 14).
	hzPerMaxBin := 30e6 / float64(1024<<14)
	spanHz := 1024 * 30e6 / (1024 * 32)
	wantStart := int(math.Round((7.2e6 - spanHz/2) / hzPerMaxBin))
	if start != wantStart {
		t.Errorf("start %d, want %d", start, wantStart)
	}
	// The window covers the target when converted through max-zoom units.
	lo := float64(start) * hzPerMaxBin
	hi := lo + spanHz
	if !(lo <= 7.2e6 && 7.2e6 <= hi) {
		t.Errorf("window %.0f-%.0f Hz misses 7.2 MHz", lo, hi)
	}
	// A target at the band edge clamps instead of running off the map.
	zoom, start = zoomWindow(10e3, 1500, 1024, 30e6, 15e6, 14)
	if start != 0 {
		t.Errorf("low edge start %d, want 0", start)
	}
	zoom, start = zoomWindow(29.99e6, 1500, 1024, 30e6, 15e6, 14)
	maxStart := 1024<<14 - 1024<<uint(14-zoom)
	if start != maxStart {
		t.Errorf("high edge start %d, want %d", start, maxStart)
	}
}

func TestParseFrameMarksSaturatedBinsInvalid(t *testing.T) {
	// Decoded dB = int8(b)+1; the server clamps to [-200, 0] dB, so
	// bytes that decode above 0 dB are artifacts and must come out NaN.
	frame := make([]byte, 16+4)
	copy(frame, "W/F\x00")
	frame[16] = 0x80 // int8 -128 -> -127 dB, valid
	frame[17] = 0xC7 // int8 -57  -> -56 dB, valid
	frame[18] = 0xFF // int8 -1   -> 0 dB, valid (clamped maximum)
	frame[19] = 0x7F // int8 127  -> +128 dB, impossible -> NaN
	got, ok := parseFrame(frame, time.Now())
	if !ok {
		t.Fatal("frame rejected")
	}
	if got.Bins[0] != -127 || got.Bins[1] != -56 || got.Bins[2] != 0 {
		t.Errorf("valid bins decoded wrong: %v", got.Bins[:3])
	}
	if !math.IsNaN(got.Bins[3]) {
		t.Errorf("saturated bin decoded as %v, want NaN", got.Bins[3])
	}
}
