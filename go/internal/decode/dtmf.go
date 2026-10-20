package decode

import (
	"strings"
)

// DTMF keypad: rows and columns, with the standard digit labels.
var (
	dtmfRows = []float64{697, 770, 852, 941}
	dtmfCols = []float64{1209, 1336, 1477, 1633}
	dtmfKeys = [4][4]byte{
		{'1', '2', '3', 'A'},
		{'4', '5', '6', 'B'},
		{'7', '8', '9', 'C'},
		{'*', '0', '#', 'D'},
	}
)

// DTMFDecode scans audio for DTMF tones and returns the digits in order.
// It follows multimon-ng's approach: per 10 ms block, compute the energy in
// each of the eight tones; a digit is present when exactly one row and one
// column tone dominate all their peers (by a factor of ten), and it is
// accepted after persisting for a couple of blocks to reject noise.
func DTMFDecode(samples []float64, rate int) string {
	if rate < 4000 || len(samples) < rate/10 {
		return ""
	}
	block := rate / 50 // 20 ms window: enough resolution to separate the
	// closely spaced row tones (697 and 770 Hz are only 73 Hz apart).
	if block < 1 {
		block = 1
	}
	// dominant returns the index of the strongest of four tones when it is
	// more than ten times every other, else -1 (multimon-ng's rule).
	dominant := func(e [4]float64) int {
		idx, best := -1, 0.0
		for i, v := range e {
			if v > best {
				best, idx = v, i
			}
		}
		if idx < 0 {
			return -1
		}
		for i, v := range e {
			if i != idx && v > best*0.1 {
				return -1
			}
		}
		return idx
	}
	var out strings.Builder
	lastKey := byte(0)
	streak := 0
	for start := 0; start+block <= len(samples); start += block {
		seg := samples[start : start+block]
		var rowE, colE [4]float64
		for i, f := range dtmfRows {
			rowE[i] = goertzel(seg, rate, f)
		}
		for i, f := range dtmfCols {
			colE[i] = goertzel(seg, rate, f)
		}
		ri, ci := dominant(rowE), dominant(colE)
		if ri < 0 || ci < 0 {
			lastKey, streak = 0, 0
			continue
		}
		key := dtmfKeys[ri][ci]
		if key == lastKey {
			if streak == 1 {
				out.WriteByte(key)
			}
			streak++
			continue
		}
		lastKey, streak = key, 1
	}
	return out.String()
}
