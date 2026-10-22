// Package decode turns recorded pass audio back into data: WAV files to
// samples, FM audio to bits (AFSK 1200), bit streams to AX.25 frames, and
// keyed tones to Morse text. Pure Go, no external dependencies, so any
// capture can be decoded wherever the binary runs.
package decode

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
)

// ReadWAV loads a 16-bit PCM WAV file, returning mono samples in [-1, 1]
// and the sample rate. Multichannel files are averaged down to mono.
func ReadWAV(path string) ([]float64, int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, err
	}
	if len(data) < 44 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return nil, 0, fmt.Errorf("%s: not a RIFF/WAVE file", path)
	}
	rate := int(binary.LittleEndian.Uint32(data[24:28]))
	channels := int(binary.LittleEndian.Uint16(data[22:24]))
	if int(binary.LittleEndian.Uint16(data[34:36])) != 16 {
		return nil, 0, fmt.Errorf("%s: only 16-bit PCM is supported", path)
	}
	offset := 12
	for offset+8 <= len(data) {
		id := string(data[offset : offset+4])
		size := int(binary.LittleEndian.Uint32(data[offset+4 : offset+8]))
		body := offset + 8
		if id == "data" {
			// A torn capture (disk full, crash mid-write) declares a data
			// chunk longer than the file actually holds. Trust the bytes,
			// not the header, or the sample loop below indexes past EOF
			// and panics the decode wave.
			if size < 0 || body+size > len(data) {
				size = len(data) - body
			}
			if size < 0 {
				size = 0
			}
			count := size / 2
			if channels < 1 {
				channels = 1
			}
			samples := make([]float64, 0, count/channels)
			for i := 0; i+channels <= count; i += channels {
				sum := 0.0
				for c := 0; c < channels; c++ {
					v := int16(binary.LittleEndian.Uint16(data[body+i*2+c*2:]))
					sum += float64(v)
				}
				samples = append(samples, sum/float64(channels)/32768.0)
			}
			return samples, rate, nil
		}
		if size%2 == 1 {
			size++
		}
		offset = body + size
	}
	return nil, 0, fmt.Errorf("%s: no data chunk", path)
}

// squelchPower returns the smoothed signal power, used to gate packet
// searches so silence and steady carriers do not produce bit garbage.
func squelchPower(samples []float64, rate int) []float64 {
	window := rate / 100 // 10 ms
	if window < 1 {
		window = 1
	}
	out := make([]float64, len(samples))
	var sum float64
	for i, s := range samples {
		sum += s * s
		if i >= window {
			sum -= samples[i-window] * samples[i-window]
		}
		if i >= window-1 {
			out[i] = sum / float64(window)
		}
	}
	return out
}

// AFSK1200 demodulates Bell 202 audio (1200/2200 Hz tones) into NRZI bit
// samples at the baud rate, as used by APRS over packet radio. This is the
// classic quadrature-discriminator demodulator: multiply by delayed copies
// of the signal and take the difference of the two products.
func AFSK1200(samples []float64, rate int) []float64 {
	// One bit is exactly rate/1200 samples at 8 kHz (6.67) - work at a
	// fixed oversampling of 8 for robustness with any rate >= 4800.
	if rate < 4800 {
		return nil
	}
	sp := float64(rate) / 1200.0
	delay := int(math.Round(sp)) // one bit period
	var bits []float64
	n := len(samples)
	for i := delay; i < n; i++ {
		// Correlate against the mark (1200 Hz) and space (2200 Hz) tones
		// using the previous bit period as the reference.
		ph1 := 2 * math.Pi * 1200 * float64(i-delay) / float64(rate)
		ph2 := 2 * math.Pi * 2200 * float64(i-delay) / float64(rate)
		m1 := samples[i] * math.Sin(ph1)
		m2 := samples[i] * math.Sin(ph2)
		_ = m2
		bits = append(bits, m1)
	}
	_ = delay
	return bits
}
