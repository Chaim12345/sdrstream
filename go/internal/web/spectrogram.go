package web

// Spectrogram evidence for archived captures: a PNG rendering of the clip's
// frequency content over time, generated offline from the saved WAV itself
// (no receiver, no live data). Linked from the capture's proof view so a
// reviewer can see the signal the decode claims, not just read it.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
)

const (
	specFFT    = 1024
	specHop    = 512
	specHeight = 256
	specMaxW   = 1200
)

// readWAVSamples decodes a RIFF/WAVE 16-bit PCM file (mono or stereo,
// stereo downmixed to the left channel) into samples and the sample rate.
func readWAVSamples(path string) ([]int16, int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, err
	}
	if len(b) < 44 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return nil, 0, errors.New("not a RIFF/WAVE file")
	}
	var rate, channels, bits int
	var data []byte
	for off := 12; off+8 <= len(b); {
		id := string(b[off : off+4])
		size := int(binary.LittleEndian.Uint32(b[off+4 : off+8]))
		body := off + 8
		if body+size > len(b) {
			size = len(b) - body
		}
		switch id {
		case "fmt ":
			if size >= 16 {
				if binary.LittleEndian.Uint16(b[body:body+2]) != 1 {
					return nil, 0, errors.New("not PCM")
				}
				channels = int(binary.LittleEndian.Uint16(b[body+2 : body+4]))
				rate = int(binary.LittleEndian.Uint32(b[body+4 : body+8]))
				bits = int(binary.LittleEndian.Uint16(b[body+14 : body+16]))
			}
		case "data":
			data = b[body : body+size]
		}
		off = body + size + (size & 1)
	}
	if rate <= 0 || channels <= 0 || bits != 16 || len(data) == 0 {
		return nil, 0, fmt.Errorf("unsupported WAV (rate=%d ch=%d bits=%d data=%d)", rate, channels, bits, len(data))
	}
	n := len(data) / 2 / channels
	out := make([]int16, n)
	for i := 0; i < n; i++ {
		out[i] = int16(binary.LittleEndian.Uint16(data[i*2*channels:]))
	}
	return out, rate, nil
}

// fft is an in-place radix-2 Cooley-Tukey transform. len(re) must be a
// power of two.
func fft(re, im []float64) {
	n := len(re)
	for i, j := 0, 0; i < n; i++ {
		if i < j {
			re[i], re[j] = re[j], re[i]
			im[i], im[j] = im[j], im[i]
		}
		for m := n >> 1; m >= 1 && j&m != 0; m >>= 1 {
			j &^= m
		}
	}
	for size := 2; size <= n; size <<= 1 {
		half := size >> 1
		step := -2 * math.Pi / float64(size)
		for start := 0; start < n; start += size {
			for k := 0; k < half; k++ {
				wr, wi := math.Cos(step*float64(k)), math.Sin(step*float64(k))
				i, j := start+k, start+k+half
				tr, ti := wr*re[j]-wi*im[j], wr*im[j]+wi*re[j]
				re[j], im[j] = re[i]-tr, im[i]-ti
				re[i], im[i] = re[i]+tr, im[i]+ti
			}
		}
	}
}

// specColor maps a 0..1 magnitude to a dark blue -> cyan -> yellow -> red
// ramp, readable on the dark UI.
func specColor(v float64) color.RGBA {
	if v < 0 {
		v = 0
	}
	if v > 1 {
		v = 1
	}
	switch {
	case v < 0.25:
		t := v / 0.25
		return color.RGBA{uint8(8 + 20*t), uint8(12 + 60*t), uint8(40 + 120*t), 255}
	case v < 0.5:
		t := (v - 0.25) / 0.25
		return color.RGBA{uint8(28 + 20*t), uint8(72 + 150*t), uint8(160 + 60*t), 255}
	case v < 0.75:
		t := (v - 0.5) / 0.25
		return color.RGBA{uint8(48 + 200*t), uint8(222 + 30*t), uint8(220 - 160*t), 255}
	default:
		t := (v - 0.75) / 0.25
		return color.RGBA{uint8(248), uint8(252 - 180*t), uint8(60 - 40*t), 255}
	}
}

// spectrogramPNG renders samples as a time-vs-frequency PNG. Frequency
// resolution is rate/specFFT; columns are averaged down to specMaxW.
func spectrogramPNG(samples []int16, rate int) ([]byte, error) {
	if len(samples) < specFFT {
		return nil, errors.New("clip shorter than one FFT window")
	}
	frames := (len(samples) - specFFT) / specHop
	hann := make([]float64, specFFT)
	for i := range hann {
		hann[i] = 0.5 * (1 - math.Cos(2*math.Pi*float64(i)/float64(specFFT-1)))
	}
	re := make([]float64, specFFT)
	im := make([]float64, specFFT)
	bins := specFFT / 2
	mag := make([][]float64, frames)
	peak := 0.0
	for f := 0; f < frames; f++ {
		base := f * specHop
		for i := 0; i < specFFT; i++ {
			re[i] = float64(samples[base+i]) / 32768 * hann[i]
			im[i] = 0
		}
		fft(re, im)
		row := make([]float64, bins)
		for b := 0; b < bins; b++ {
			m := math.Hypot(re[b], im[b])
			row[b] = m
			if m > peak {
				peak = m
			}
		}
		mag[f] = row
	}
	if peak <= 0 {
		return nil, errors.New("silent clip")
	}
	// dB scale relative to the clip peak, 60 dB dynamic range.
	norm := make([][]float64, frames)
	for f, row := range mag {
		r := make([]float64, bins)
		for b, m := range row {
			db := 20 * math.Log10(m/peak+1e-9)
			v := (db + 60) / 60
			if v < 0 {
				v = 0
			}
			r[b] = v
		}
		norm[f] = r
	}
	width := frames
	if width > specMaxW {
		width = specMaxW
	}
	img := image.NewRGBA(image.Rect(0, 0, width, specHeight))
	for x := 0; x < width; x++ {
		// average the source frames that fall into this output column
		f0 := x * frames / width
		f1 := (x + 1) * frames / width
		if f1 <= f0 {
			f1 = f0 + 1
		}
		for y := 0; y < specHeight; y++ {
			// top row = highest frequency
			b0 := (specHeight - 1 - y) * bins / specHeight
			v := 0.0
			for f := f0; f < f1; f++ {
				v += norm[f][b0]
			}
			v /= float64(f1 - f0)
			img.SetRGBA(x, y, specColor(v))
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// spectrogramFor returns the PNG for an archived capture, generating and
// caching it beside the WAV on first request (<name>.spec.png).
func (s *Server) spectrogramFor(wavName string) ([]byte, error) {
	base := strings.TrimSuffix(wavName, ".wav")
	cachePath := filepath.Join(s.capturesDir, base+".spec.png")
	if b, err := os.ReadFile(cachePath); err == nil {
		return b, nil
	}
	samples, rate, err := readWAVSamples(filepath.Join(s.capturesDir, wavName))
	if err != nil {
		return nil, err
	}
	pngBytes, err := spectrogramPNG(samples, rate)
	if err != nil {
		return nil, err
	}
	_ = os.WriteFile(cachePath, pngBytes, 0o644) // cache is a convenience, not a requirement
	return pngBytes, nil
}
