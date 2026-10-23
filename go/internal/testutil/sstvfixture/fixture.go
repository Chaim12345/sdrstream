// Package sstvfixture synthesizes a complete Martin M1 SSTV frame as
// 22050 Hz audio: calibration header, VIS code, and all 256 lines with
// per-line sync, so decoder tests can verify full-frame geometry and
// pixel fidelity without a live RF capture. The signal is
// phase-continuous FM, the same shape the demodulator recovers from
// real audio.
package sstvfixture

import "math"

const (
	Rate   = 22050
	Width  = 320
	Height = 256

	leaderSec = 0.300
	breakSec  = 0.010
	bitSec    = 0.030
	syncSec   = 4.862e-3
	gapSec    = 0.572e-3
	scanSec   = 146.432e-3

	fLeader = 1900.0
	fSync   = 1200.0
	fGap    = 1500.0
	fBit0   = 1300.0
	fBit1   = 1100.0

	visMartinM1 = 0x2C
)

func freqFor(v uint8) float64 { return 1500 + float64(v)*800/255 }

// EncodeMartinM1 renders pixel(x, y) into a full Martin M1 frame.
// Channel order on the wire is green, blue, red, matching the Martin
// family line layout.
func EncodeMartinM1(pixel func(x, y int) (r, g, b uint8)) []float64 {
	type seg struct {
		freq float64 // Hz; <0 means sweep across one scan line
		dur  float64
		y    int
		ch   int // 0=g, 1=b, 2=r when freq<0
	}
	segs := []seg{{fLeader, leaderSec, 0, 0}, {fSync, breakSec, 0, 0}, {fLeader, leaderSec, 0, 0}}
	// VIS: start bit, 7 data bits LSB first, even parity bit, stop bit.
	visBits := []int{0}
	ones := 0
	for i := 0; i < 7; i++ {
		b := (visMartinM1 >> i) & 1
		ones += b
		visBits = append(visBits, b)
	}
	visBits = append(visBits, ones&1, 0) // even parity, stop bit
	for i, b := range visBits {
		f := fBit0
		if i == 0 || i == len(visBits)-1 {
			f = fSync // start and stop bits are 1200 Hz
		} else if b == 1 {
			f = fBit1
		}
		segs = append(segs, seg{f, bitSec, 0, 0})
	}
	for y := 0; y < Height; y++ {
		segs = append(segs,
			seg{fSync, syncSec, y, 0}, seg{fGap, gapSec, y, 0},
			seg{-1, scanSec, y, 0}, seg{fGap, gapSec, y, 0},
			seg{-1, scanSec, y, 1}, seg{fGap, gapSec, y, 0},
			seg{-1, scanSec, y, 2}, seg{fGap, gapSec, y, 0},
		)
	}
	total := 0.0
	for _, s := range segs {
		total += s.dur
	}
	out := make([]float64, int(total*Rate)+1)
	pos := 0
	phase := 0.0
	for _, s := range segs {
		n := int(s.dur * Rate)
		for i := 0; i < n && pos < len(out); i++ {
			f := s.freq
			if f < 0 {
				x := int(float64(i) / s.dur / Rate * Width)
				if x >= Width {
					x = Width - 1
				}
				r, g, b := pixel(x, s.y)
				switch s.ch {
				case 0:
					f = freqFor(g)
				case 1:
					f = freqFor(b)
				default:
					f = freqFor(r)
				}
			}
			phase += 2 * math.Pi * f / Rate
			out[pos] = 0.8 * math.Cos(phase)
			pos++
		}
	}
	return out[:pos]
}
