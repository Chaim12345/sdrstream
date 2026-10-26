package iqsrc

import (
	"context"
	"encoding/binary"
	"testing"
	"time"
)

// The settings command order must match the reference client: format,
// decimation, frequency, streaming mode, [gain, digital gain], then enable.
// Order is observable server-side behavior, not an internal detail.
func TestSpySettingsOrderMatchesReference(t *testing.T) {
	f := newFakeSpy(t)
	defer f.close()
	c, err := DialSpy(context.Background(), SpyConfig{Addr: f.ln.Addr().String(), Frequency: 97000000, SampleRate: 2500000, Gain: 10, DigitalGain: true, AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	<-f.enabled // server-side read loop has consumed everything
	got := f.settingsCopy()
	want := [][2]uint32{
		{spySettingIQFormat, spyFormatUint8},
		{spySettingIQDecimation, 1}, // 10M max rate /2^1 = 5M <= ... fake max 10M: decimation for <=2.5M is 2 (10M/4)
		{spySettingIQFrequency, 97000000},
		{spySettingStreamingMode, spyStreamModeIQOnly},
		{spySettingGain, 10},
		{spySettingIQDigitalGain, 6}, // RTLSDR fake: uint32(2*3.01)
	}
	// decimation index: largest rate <= 2.5M from 10M/2^d = 2 (2.5M exactly)
	want[1][1] = 2
	if len(got) < len(want)+1 {
		t.Fatalf("only %d settings recorded: %v", len(got), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("setting %d = %v, want %v (full: %v)", i, got[i], w, got)
		}
	}
	if got[len(want)] != [2]uint32{spySettingStreamingEnabled, 1} {
		t.Fatalf("last setting must be enable: %v", got[len(want)])
	}
}

// A mid-stream CLIENT_SYNC must update the readback state: control truth
// comes from what the server reports applied, not what we requested.
func TestSpyMidStreamSyncUpdatesReadback(t *testing.T) {
	f := newFakeSpy(t)
	defer f.close()
	f.sync.IQCenterFrequency = 97000000 // handshake claims one center
	c, err := DialSpy(context.Background(), SpyConfig{Addr: f.ln.Addr().String(), Frequency: 97000000, AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// Server then streams: first an updated CLIENT_SYNC reporting a
	// DIFFERENT applied center, then an IQ message.
	cs := make([]byte, 40)
	for i, v := range []uint32{1, 10, 87700000, 87700000, 0, 0, 0, 0, 0} {
		binary.LittleEndian.PutUint32(cs[4*i:], v)
	}
	m := make([]byte, 20+len(cs))
	putMsg(m[:20], spyMsgClientSync, 0, 7, cs)
	copy(m[20:], cs)
	f.streamRaw <- m
	f.stream <- make([]byte, 64) // one small IQ message to unblock ReadSamples
	buf := make([]complex64, 64)
	if _, err := c.ReadSamples(buf); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		sync, have := c.SyncSnapshot()
		if have && sync.IQCenterFrequency == 87700000 {
			return // readback updated
		}
		if time.Now().After(deadline) {
			t.Fatalf("readback not updated: %+v have=%v", sync, have)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
