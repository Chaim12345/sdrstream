package decode

import (
	"fmt"
	"math"
	"sort"
)

// Transmission is one detected signal event in a recording: when it
// started, how long it lasted, what it looked like (tone, packet-like
// keying, or voice-like), and how strong it was.
type Transmission struct {
	StartSec    float64
	DurationSec float64
	PeakDB      float64
	ToneHz      float64
	Score       float64
	Description string
}

// ScanTransmissions finds every burst of signal in the audio. Because
// recordings come from AGC receivers, level is useless; activity is
// detected by a drop in spectral flatness, which is gain-invariant.
// Bursts are then classified as data, CW, or voice.
func ScanTransmissions(path string) ([]Transmission, error) {
	samples, rate, err := ReadWAV(path)
	if err != nil {
		return nil, err
	}
	step := rate / 10 // 100 ms windows
	if step < 64 {
		step = 64
	}
	var flats []float64
	for start := 0; start+step <= len(samples); start += step {
		f, _, _ := spectrum(samples[start:start+step], rate)
		flats = append(flats, f)
	}
	if len(flats) == 0 {
		return nil, nil
	}
	// A window is active when its spectrum is concentrated. Noise on an
	// AGC receiver sits near 0.15-0.35; a real emission drops it below
	// the threshold.
	const active = 0.12
	var out []Transmission
	start := -1
	for i, f := range flats {
		if f < active {
			if start < 0 {
				start = i
			}
		} else if start >= 0 {
			out = append(out, describe(samples, rate, step, start, i))
			start = -1
		}
	}
	if start >= 0 {
		out = append(out, describe(samples, rate, step, start, len(flats)))
	}
	return out, nil
}

// describe classifies one detected burst, given window indices [from, to)
// into the step-sample envelope.
func describe(samples []float64, rate, step, from, to int) Transmission {
	unit := step
	lo := from * unit
	hi := to * unit
	if hi > len(samples) {
		hi = len(samples)
	}
	t := Transmission{StartSec: float64(lo) / float64(rate)}
	if hi > lo {
		t.DurationSec = float64(hi-lo) / float64(rate)
	}
	// Peak level within the burst.
	peak := -100.0
	for i := lo; i < hi; i += unit / 4 {
		if i >= len(samples) {
			break
		}
		v := 20 * math.Log10(math.Abs(samples[i])+1e-9)
		if v > peak {
			peak = v
		}
	}
	t.PeakDB = peak

	seg := samples
	if hi <= len(seg) && hi > lo {
		seg = seg[lo:hi]
	}
	if len(seg) < rate/8 {
		t.Description = "short burst"
		return t
	}
	tone, snr := FindDominantTone(seg, rate)
	t.ToneHz = tone
	t.Score = snr
	switch {
	case snr > 20 && t.DurationSec > 1.5 && tone > 200:
		// Strong steady tone with keying: check the envelope for on/off.
		if isOnOffKeyed(seg, rate, tone) {
			t.Description = fmt.Sprintf("CW/morse @ %.0f Hz", tone)
		} else {
			t.Description = fmt.Sprintf("steady tone @ %.0f Hz (carrier or beacon)", tone)
		}
	case snr > 8 && t.DurationSec < 2.0:
		t.Description = "packet/data burst (AFSK or digital)"
	default:
		if t.DurationSec > 2.0 {
			t.Description = "voice/SSB or mixed traffic"
		} else {
			t.Description = "noise burst"
		}
	}
	return t
}

// isOnOffKeyed reports whether the tone envelope toggles on and off at
// Morse-like rates, as opposed to a continuously keyed carrier.
func isOnOffKeyed(seg []float64, rate int, tone float64) bool {
	win := rate / 100
	if win < 1 {
		win = 1
	}
	var env []float64
	for start := 0; start+win <= len(seg); start += win {
		env = append(env, goertzel(seg[start:start+win], rate, tone))
	}
	if len(env) < 10 {
		return false
	}
	sorted := append([]float64(nil), env...)
	sort.Float64s(sorted)
	floor := sorted[len(sorted)/4]
	peak := sorted[3*len(sorted)/4]
	if peak <= floor {
		return false
	}
	threshold := math.Sqrt(floor * peak)
	toggles := 0
	keyed := env[0] > threshold
	for _, v := range env {
		now := v > threshold
		if now != keyed {
			toggles++
			keyed = now
		}
	}
	// Morse at 15-25 wpm toggles several times a second.
	perSec := float64(toggles) / (float64(len(env)) * float64(win) / float64(rate))
	return perSec > 0.5 && perSec < 30
}
