package decode

import (
	"fmt"
)

// SSTV (slow-scan television) sends a picture as an audio tone whose
// frequency maps to brightness. Every transmission begins with a VIS
// (Vertical Interval Signalling) header that names the mode, so a decoder
// can tell a real image from noise before spending time on the picture.
//
// VIS format: a 1200 Hz leader (~300 ms), a 1900 Hz break (~10 ms), a
// 1100 Hz start bit, seven data bits LSB first (1300 Hz = 1, 1100 Hz = 0),
// an even-parity bit, then a 1200 Hz stop bit. Each of the start, data,
// parity and stop bits lasts 30 ms.
const (
	sstvLeader = 1200.0
	sstvBreak  = 1900.0
	sstvBit0   = 1100.0
	sstvBit1   = 1300.0
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
	// Slide in 10 ms steps looking for the 1900 Hz break tone that follows
	// the 1200 Hz leader.
	step := rate / 100
	if step < 1 {
		step = 1
	}
	window := rate / 50 // 20 ms
	// The leader must be present for roughly 300 ms before the break tone.
	leaderWindow := 3 * rate / 10
	for start := 0; start+window < len(samples); start += step {
		seg := samples[start : start+window]
		brk := goertzel(seg, rate, sstvBreak)
		ldr := goertzel(seg, rate, sstvLeader)
		if brk <= ldr*4 {
			continue
		}
		// The break tone must dominate the window; a real 1900 Hz burst is
		// nearly the only energy present for its 10 ms.
		var winPower float64
		for _, s := range seg {
			winPower += s * s
		}
		if winPower <= 0 || (brk*brk)/winPower < float64(len(seg))/8 {
			continue
		}
		// Require a preceding 1200 Hz leader; random audio rarely shows a
		// sustained pure tone just before a strong 1900 Hz burst.
		leadStart := start - leaderWindow
		if leadStart < 0 {
			leadStart = 0
		}
		lead := samples[leadStart:start]
		if !hasLeader(lead, rate) {
			continue
		}
		// Found a break tone; read the bits that follow it.
		base := start + window
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

// hasLeader reports whether a segment is dominated by the 1200 Hz SSTV
// leader tone, measured as the leader energy relative to the segment's
// total energy.
func hasLeader(seg []float64, rate int) bool {
	if len(seg) < rate/50 {
		return false
	}
	lead := goertzel(seg, rate, sstvLeader)
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

// readVIS reads a start bit, seven data bits LSB first and an even parity
// bit starting at sample offset base. It returns the VIS code.
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
	if start, ok := readBit(0); !ok || start != 0 {
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
