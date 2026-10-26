package decode

import "math"

// fmstereo.go: broadcast-FM stereo decoder for the composite (MPX) signal a
// wideband FM discriminator outputs: L+R at baseband, a 19 kHz pilot, and
// L-R as DSB-SC around 38 kHz. Input is the MPX waveform at rate >= 76 kHz
// (>= 100 kHz recommended). It is a pure function of samples and does not
// touch any receiver path. Written from the FM stereo (pilot-tone system)
// definition; no upstream code copied.

// StereoResult is the decoded stereo audio plus lock diagnostics.
type StereoResult struct {
	L, R        []float64
	PilotLocked bool
	PilotSNRdB  float64 // pilot power vs 16-18/20-22 kHz neighbourhood
	PilotPhase  float64
}

func goertzelPower(x []float64, rate int, f float64) float64 {
	w := 2 * math.Pi * f / float64(rate)
	var re, im float64
	for i, v := range x {
		re += v * math.Cos(w*float64(i))
		im += v * math.Sin(w*float64(i))
	}
	n := float64(len(x))
	return (re*re + im*im) / (n * n)
}

// onePole low-pass applied k times (a cheap steep-enough filter for audio).
func lowpass(x []float64, rate int, fc float64, k int) []float64 {
	a := 1 - math.Exp(-2*math.Pi*fc/float64(rate))
	out := append([]float64{}, x...)
	for ; k > 0; k-- {
		var y float64
		for i, v := range out {
			y += a * (v - y)
			out[i] = y
		}
	}
	return out
}

// DecodeFMStereo returns L/R. If no 19 kHz pilot stands out by at least
// 12 dB over its neighbours, it returns mono (L=R=L+R) with PilotLocked
// false rather than inventing a stereo difference.
func DecodeFMStereo(mpx []float64, rate int) StereoResult {
	var res StereoResult
	if rate < 76000 || len(mpx) < rate/20 {
		return res
	}
	pil := goertzelPower(mpx, rate, 19000)
	nb := (goertzelPower(mpx, rate, 17000) + goertzelPower(mpx, rate, 21000)) / 2
	if nb <= 0 {
		nb = 1e-18
	}
	res.PilotSNRdB = 10 * math.Log10(pil/nb+1e-18)
	sum := lowpass(mpx, rate, 15000, 3)
	res.L = make([]float64, len(mpx))
	res.R = make([]float64, len(mpx))
	if res.PilotSNRdB < 12 {
		copy(res.L, sum)
		copy(res.R, sum)
		return res
	}
	res.PilotLocked = true
	// Pilot phase: measure it over the whole buffer (feedforward, like
	// bpsk.go); a 38 kHz carrier is the pilot doubled in phase.
	w := 2 * math.Pi * 19000 / float64(rate)
	var re, im float64
	for i, v := range mpx {
		re += v * math.Cos(w*float64(i))
		im -= v * math.Sin(w*float64(i))
	}
	ph := math.Atan2(im, re) // pilot = A cos(w t + ph)
	res.PilotPhase = ph
	diff := make([]float64, len(mpx))
	for i, v := range mpx {
		diff[i] = -v * 2 * math.Sin(2*(w*float64(i)+ph)) // sin(2*pilot) with pilot=sin(theta)
	}
	diff = lowpass(diff, rate, 15000, 3)
	for i := range sum {
		res.L[i] = (sum[i] + diff[i]) / 2
		res.R[i] = (sum[i] - diff[i]) / 2
	}
	return res
}
