package kiwisdr

// Beacon SNR probe. A receiver's health table entry includes the SNR of a
// known, always-on station as heard through that receiver: continuous
// time-signal carriers (WWV/BPM/RWM share the ITU standard frequencies) are
// the reference. The receiver is tuned in USB below the carrier so it lands
// at a fixed audio tone, and a Goertzel measurement at the tone against the
// neighbouring noise floor yields the SNR estimate.

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"
)

// BeaconTarget names a known station carrier and where it lands in the
// audio passband when the receiver is tuned to DialKHz in USB.
type BeaconTarget struct {
	Name    string
	DialKHz float64 // USB dial; carrier sits at ToneHz above it
	ToneHz  float64 // expected audio offset of the carrier
}

// DefaultBeacons are continuous standard-frequency/time-signal carriers.
// Any receiver covering HF can hear at least one of them under normal
// propagation; the dial offsets place the carrier at a 700 Hz tone.
var DefaultBeacons = []BeaconTarget{
	{Name: "WWV/BPM/RWM 10 MHz", DialKHz: 10000 - 0.7, ToneHz: 700},
	{Name: "WWV/BPM 15 MHz", DialKHz: 15000 - 0.7, ToneHz: 700},
	{Name: "WWV/BPM 5 MHz", DialKHz: 5000 - 0.7, ToneHz: 700},
}

// beaconListen bounds one SNR measurement: long enough for a stable
// estimate, short enough to stay a polite guest on a shared receiver.
const (
	beaconListen    = 5 * time.Second
	beaconListenMax = 10 * time.Second
)

// goertzel returns the signal power at freqHz over the whole buffer.
func goertzel(samples []int16, rate int, freqHz float64) float64 {
	n := len(samples)
	if n == 0 || rate <= 0 {
		return 0
	}
	w := 2 * math.Pi * freqHz / float64(rate)
	cc := 2 * math.Cos(w)
	var s0, s1, s2 float64
	for _, v := range samples {
		s0 = float64(v) + cc*s1 - s2
		s2 = s1
		s1 = s0
	}
	power := s1*s1 + s2*s2 - cc*s1*s2
	return power / (float64(n) * float64(n))
}

// noiseOffsets are the audio offsets (relative to the tone) whose median
// Goertzel power estimates the noise floor. They stay inside a typical
// 300-2700 Hz USB passband and clear of the tone itself.
var noiseOffsets = []float64{-250, -150, 150, 250, 350, 500}

// EstimateToneSNR estimates how far a tone at toneHz stands above the
// neighbouring noise floor, in dB: tone-bin power (minus the floor) against
// the median power of six neighbouring bins. Tone and floor bins share the
// same Goertzel analysis bandwidth (~rate/n Hz), so their ratio needs no
// bandwidth scaling. An earlier version subtracted a
// 10*log10(2*rate/(n*500)) "500 Hz normalisation" - about 31 dB at the
// fixed 5 s/12 kHz listen - which treated the tone bin as if its energy
// were spread over 500 Hz while the floor bins stayed single-bin
// estimates. That term was miscalibrated: on a live receiver it turned a
// carrier +17.3 dB over the floor (FFT of the same capture) into a -23.2
// dB reading, dragging every health-table score toward zero.
//
// The estimate is a per-analysis-bin figure, not a 500 Hz-normalised
// carrier SNR: a longer capture narrows the bin and reads higher on the
// same signal, so receivers are only comparable at the same listen time
// (MeasureBeaconSNR fixes it). The figure matches what an FFT of the same
// recording shows. It is still an approximation - on-off keyed or ticking
// carriers (RWM keys its carrier; time signals tick once per second) read
// low by the duty cycle - but it ranks receivers consistently, which is
// what the health table needs. ok is false when the buffer is too short to
// say anything.
func EstimateToneSNR(samples []int16, rate int, toneHz float64) (snrDB float64, ok bool) {
	if rate <= 0 || toneHz <= 0 || len(samples) < rate/2 {
		return 0, false
	}
	tone := goertzel(samples, rate, toneHz)
	noise := make([]float64, 0, len(noiseOffsets))
	for _, off := range noiseOffsets {
		f := toneHz + off
		if f <= 0 || f >= float64(rate)/2 {
			continue
		}
		noise = append(noise, goertzel(samples, rate, f))
	}
	if len(noise) == 0 {
		return 0, false
	}
	sort.Float64s(noise)
	floor := noise[len(noise)/2]
	if floor <= 0 {
		floor = 1e-12
	}
	// Tone bin holds signal+noise; subtract the floor so a pure-noise
	// recording floors instead of reading 0 dB "signal". The ratio is the
	// estimate: both bins share the analysis bandwidth, so no further
	// scaling applies.
	excess := tone - floor
	if excess < floor*1e-6 {
		excess = floor * 1e-6
	}
	snr := 10 * math.Log10(excess/floor)
	return snr, true
}

// MeasureBeaconSNR connects to host, tunes the beacon carrier in USB, records
// a short segment and returns its estimated SNR in dB. One measurement holds
// one receiver connection for listen (<=10s, capped); callers pick the cadence
// so a shared public receiver is never hammered.
func MeasureBeaconSNR(ctx context.Context, host string, beacon BeaconTarget, listen time.Duration, opts Options) (float64, error) {
	if beacon.ToneHz <= 0 || beacon.DialKHz <= 0 {
		return 0, errors.New("beacon target needs DialKHz and ToneHz")
	}
	if listen <= 0 {
		listen = beaconListen
	}
	listen = min(listen, beaconListenMax)
	c, err := New(host, opts)
	if err != nil {
		return 0, err
	}
	defer c.Close()
	if err := c.Connect(ctx); err != nil {
		return 0, fmt.Errorf("connect: %w", err)
	}
	if err := c.Tune(ctx, int64(beacon.DialKHz*1000), "usb"); err != nil {
		return 0, fmt.Errorf("tune: %w", err)
	}
	samples, rate, err := c.Record(ctx, listen)
	if err != nil {
		return 0, fmt.Errorf("record: %w", err)
	}
	snr, ok := EstimateToneSNR(samples, rate, beacon.ToneHz)
	if !ok {
		return 0, fmt.Errorf("recording too short for an SNR estimate (%d samples at %d Hz)", len(samples), rate)
	}
	return snr, nil
}
