package iqdemod

import "math"

// Deemphasis applies the broadcast-FM pre/de-emphasis time constant to ONE
// audio channel AFTER the stereo matrix (never to the composite MPX: the
// pilot and the 38 kHz subcarrier region must reach the stereo decoder
// unshaped). tau is 75e-6 (Americas) or 50e-6 (most of the world).
// y[n] = y[n-1] + a*(x[n]-y[n-1]), a = 1-exp(-1/(rate*tau)); -3 dB at
// 1/(2*pi*tau) (2122 Hz at 75us, 3183 Hz at 50us).
func Deemphasis(x []float64, rate int, tau float64) []float64 {
	a := 1 - math.Exp(-1/(float64(rate)*tau))
	out := make([]float64, len(x))
	var y float64
	for i, v := range x {
		y += a * (v - y)
		out[i] = y
	}
	return out
}
