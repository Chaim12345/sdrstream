package decode

import (
	"fmt"
)

// SSTV (slow-scan television) sends a picture as an audio tone whose
// frequency maps to brightness. Every transmission begins with a VIS
// (Vertical Interval Signalling) header that names the mode, so a decoder
// can tell a real image from noise before spending time on the picture.
//
// VIS format: a 1900 Hz leader (~300 ms), a 1200 Hz break (~10 ms), a
// second 1900 Hz leader (~300 ms), then nine 30 ms signalling tones: a
// 1100 Hz start bit, seven data bits LSB first (1100 Hz = 1, 1300 Hz = 0),
// an even-parity bit over the data bits, and a 1200 Hz stop bit.
const (
	sstvLeader = 1900.0
	sstvBreak  = 1200.0
	sstvBit0   = 1300.0
	sstvBit1   = 1100.0
	sstvBitLen = 0.030 // seconds
)

// sstvModes maps a VIS code to a human mode name.
var sstvModes = map[int]string{
	0x00: "Robot 36", 0x01: "Robot 12", 0x02: "Robot 24", 0x03: "Robot 8",
	0x04: "Robot 24 BW", 0x08: "Robot 8 BW",
	0x28: "Scottie S1", 0x2C: "Scottie S2", 0x30: "Scottie DX",
	0x5C: "Scottie S3", 0x5D: "Scottie S4", 0x5F: "Scottie S5",
	0x38: "Martin M1", 0x3C: "Martin M2", 0x40: "Martin M3", 0x44: "Martin M4",
	0x60: "PD 50", 0x61: "PD 90", 0x62: "PD 120", 0x63: "PD 160",
	0x64: "PD 180", 0x65: "PD 240", 0x66: "PD 290", 0x76: "PD 160",
	0x8C: "Scottie S1 (alt)", 0xAC: "Martin M1 (alt)", 0xB0: "Wraase SC2-120",
}

// SSTVResult is a detected SSTV header.
type SSTVResult struct {
	Mode     string
	VIS      int
	StartSec float64
	SnrdB    float64
}

// SSTVDetect scans audio for SSTV VIS headers and returns each one found
// with the mode it names.
func SSTVDetect(samples []float64, rate int) []SSTVResult {
	if rate < 8000 || len(samples) < rate/2 {
		return nil
	}
	bitSamples := int(sstvBitLen * float64(rate))
	if bitSamples < 8 {
		return nil
	}
	var out []SSTVResult
	// The calibration header is leader1(1900, ~300 ms), break(1200, 10 ms),
	// leader2(1900, ~300 ms), then the VIS bits. A break candidate wins
	// only when its own 10 ms window is pure 1200 Hz and both flanking
	// 300 ms windows are pure 1900 Hz.
	breakWindow := rate / 100 // 10 ms
	leaderWindow := 3 * rate / 10
	// Advance by an eighth of the break so some window is always at least
	// 7/8 inside a 10 ms break, regardless of where it starts.
	step := breakWindow / 8
	if step < 1 {
		step = 1
	}
	for start := breakWindow; start+breakWindow <= len(samples); start += step {
		brk := samples[start : start+breakWindow]
		var segPower float64
		for _, s := range brk {
			segPower += s * s
		}
		if segPower <= 0 {
			continue
		}
		g1200 := goertzel(brk, rate, sstvBreak)
		g1900 := goertzel(brk, rate, sstvLeader)
		if g1200 <= g1900*4 || (g1200*g1200)/segPower < float64(len(brk))/8 {
			continue
		}
		leadStart := start - leaderWindow
		if leadStart < 0 || start+leaderWindow >= len(samples) {
			continue
		}
		// Both the leader1 block before and the leader2 block after must
		// be dominated by 1900 Hz; random audio never shows two long pure
		// tones around a 10 ms spawn.
		if !hasLeader(samples[leadStart:start], rate, sstvLeader) {
			continue
		}
		if !hasLeader(samples[start+breakWindow:start+leaderWindow+breakWindow], rate, sstvLeader) {
			continue
		}
		// Found the header; the VIS bits start after leader 2.
		base := start + breakWindow + leaderWindow
		vis, ok := readVIS(samples, rate, base, bitSamples)
		if !ok {
			continue
		}
		name, known := sstvModes[vis]
		if !known {
			name = fmt.Sprintf("VIS 0x%02X", vis)
		}
		out = append(out, SSTVResult{
			Mode:     name,
			VIS:      vis,
			StartSec: float64(start) / float64(rate),
		})
		// Skip past this header so one break is not counted twice.
		start = base + 9*bitSamples
	}
	return out
}

// hasLeader reports whether a segment is dominated by the given tone.
func hasLeader(seg []float64, rate int, leaderFreq float64) bool {
	if len(seg) < rate/50 {
		return false
	}
	lead := goertzel(seg, rate, leaderFreq)
	var power float64
	for _, s := range seg {
		power += s * s
	}
	if power <= 0 {
		return false
	}
	// A pure sine of amplitude A over N samples has energy N*A^2/2. The
	// Goertzel magnitude is ~N*A/2, so its square over the total is ~N/2.
	ratio := (lead * lead) / power
	return ratio > float64(len(seg))/8
}

// readVIS reads a 1100 Hz start bit, seven data bits LSB first, an even
// parity bit and a 1200 Hz stop bit starting at sample offset base. It
// returns the VIS code.
func readVIS(samples []float64, rate, base, bitSamples int) (int, bool) {
	readBit := func(i int) (int, bool) {
		from := base + i*bitSamples
		to := from + bitSamples
		if to > len(samples) {
			return 0, false
		}
		seg := samples[from:to]
		zero := goertzel(seg, rate, sstvBit0)
		one := goertzel(seg, rate, sstvBit1)
		if one > zero {
			return 1, true
		}
		return 0, true
	}
	if start, ok := readBit(0); !ok || start != 1 {
		return 0, false
	}
	vis := 0
	for i := 0; i < 7; i++ {
		b, ok := readBit(i + 1)
		if !ok {
			return 0, false
		}
		vis |= b << i
	}
	parity, ok := readBit(8)
	if !ok {
		return 0, false
	}
	// Even parity over the seven data bits must match the parity bit.
	ones := 0
	for i := 0; i < 7; i++ {
		ones += (vis >> i) & 1
	}
	if (ones+parity)%2 != 0 {
		return 0, false
	}
	return vis, true
}
