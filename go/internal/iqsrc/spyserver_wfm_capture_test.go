package iqsrc

// TestLiveWFMCapture records ONE bounded channel-rate IQ capture from a
// public SpyServer for offline WFM/stereo/de-emphasis analysis. Gated on
// SDRSTREAM_LIVE_WFM_CAP="host:port,freq[,secs]" (secs <= 20). The raw
// capture is PRIVATE: it lands in SDRSTREAM_LIVE_WFM_CAP_OUT (a local dir),
// never in the repo. Facts recorded next to it as JSON: negotiated rate,
// HaveSync, server-reported centers when volunteered (UNKNOWN otherwise),
// CanControl, requested frequency, IQ message count, sequence gaps, wall
// time. No A/B claims; this is the matched-mode capture for a listening
// test.

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestLiveWFMCapture(t *testing.T) {
	spec := os.Getenv("SDRSTREAM_LIVE_WFM_CAP")
	outDir := os.Getenv("SDRSTREAM_LIVE_WFM_CAP_OUT")
	if spec == "" || outDir == "" {
		t.Skip("set SDRSTREAM_LIVE_WFM_CAP and SDRSTREAM_LIVE_WFM_CAP_OUT")
	}
	parts := strings.Split(spec, ",")
	if len(parts) < 2 {
		t.Fatalf("spec host:port,freq[,secs]")
	}
	addr := parts[0]
	freq64, err := strconv.ParseUint(parts[1], 10, 32)
	if err != nil {
		t.Fatal(err)
	}
	secs := 15.0
	if len(parts) > 2 {
		secs, err = strconv.ParseFloat(parts[2], 64)
		if err != nil || secs <= 0 || secs > 20 {
			t.Fatalf("secs must be (0,20], got %q", parts[2])
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(secs+20)*time.Second)
	defer cancel()
	// Channel rate appropriate to a broadcast channel: ask 2.5M, accept the
	// server's negotiated decimation (Airspy One gives 1.5M).
	c, err := DialSpy(ctx, SpyConfig{Addr: addr, Frequency: uint32(freq64), SampleRate: 2500000, DigitalGain: true, AllowPrivate: true})
	if err != nil {
		t.Fatalf("DialSpy: %v", err)
	}
	defer c.Close()
	rate := int(c.SampleRate())
	if rate < 250000 {
		t.Fatalf("negotiated rate %d too low for a broadcast channel", rate)
	}
	outPath := filepath.Join(outDir, fmt.Sprintf("wfm-cap-%s-%d.f32", strings.ReplaceAll(addr, ":", "_"), time.Now().Unix()))
	f, err := os.Create(outPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	buf := make([]complex64, 65536)
	var total int64
	var maxBuf [8]byte
	start := time.Now()
	for time.Since(start) < time.Duration(secs*float64(time.Second)) {
		n, err := c.ReadSamples(buf)
		if err != nil {
			t.Logf("stream ended early: %v", err)
			break
		}
		for i := 0; i < n; i++ {
			binary.LittleEndian.PutUint32(maxBuf[0:4], math32bits(real(buf[i])))
			binary.LittleEndian.PutUint32(maxBuf[4:8], math32bits(imag(buf[i])))
			f.Write(maxBuf[:])
		}
		total += int64(n)
	}
	wall := time.Since(start)
	st := c.Stats()
	facts := map[string]interface{}{
		"addr": addr, "requestedFreqHz": freq64, "negotiatedRateHz": rate,
		"haveSync": c.HaveSync, "wallSec": wall.Seconds(), "samples": total,
		"iqMessages": st.IQMessages, "iqSeqGaps": st.IQSeqGaps,
		"captureFile": outPath, "format": "interleaved float32 IQ",
	}
	if c.HaveSync {
		facts["serverReportedCanControl"] = c.Sync.CanControl
		facts["serverReportedDeviceCenterHz"] = c.Sync.DeviceCenterFrequency
		facts["serverReportedIQCenterHz"] = c.Sync.IQCenterFrequency
	} else {
		facts["serverReportedCenterHz"] = "UNKNOWN (no CLIENT_SYNC volunteered)"
	}
	fj, _ := json.MarshalIndent(facts, "", "  ")
	os.WriteFile(outPath+".facts.json", fj, 0600)
	t.Logf("CAPTURE: %s", fj)
	cov := float64(total) / (wall.Seconds() * float64(rate)) * 100
	t.Logf("coverage=%.0f%% (%d samples / %.1fs at %dHz)", cov, total, wall.Seconds(), rate)
}

func math32bits(f float32) uint32 { return math.Float32bits(f) }
