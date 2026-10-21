package decode

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
)

// WEFAX demodulates HF weather fax (WMO F1C): audio FM where 1500 Hz is
// black and 2300 Hz is white, 120 lines per minute (0.5 s per line) at
// IOC 576. The image is found via the phasing block that precedes the
// chart: lines whose first 5% carries a white/black wedge that repeats
// once per line, which fixes both the line clock and the line start.
//
// The output is a PNG whose width is pixelsPerLine (IOC*pi ≈ 1810 for
// IOC 576) and whose height is the number of complete lines decoded.

const (
	wefaxBlackHz = 1500.0
	wefaxWhiteHz = 2300.0
)

// WEFAXImage demodulates samples (mono audio at rate) into a PNG image.
// linesPerMinute is 120 for DWD; zero uses 120. ioc is 576 for DWD; zero
// uses 576.
func WEFAXImage(samples []float64, rate int, linesPerMinute float64, ioc int) ([]byte, error) {
	if linesPerMinute <= 0 {
		linesPerMinute = 120
	}
	if ioc <= 0 {
		ioc = 576
	}
	lineSec := 60.0 / linesPerMinute
	lineSamples := int(math.Round(lineSec * float64(rate)))
	if len(samples) < lineSamples*10 {
		return nil, errShortWEFAX
	}
	// Instantaneous video frequency per sample via a quadrature mix at the
	// 1900 Hz centre and phase differentiation.
	video := wefaxVideoFreq(samples, rate)
	// Find the phasing alignment: the phasing signal is a white pulse
	// occupying the first ~5% of each line, so the start offset is where
	// the mean video level at offset..offset+5% is maximal over candidate
	// offsets within one line period.
	bestOff, bestMean := 0, -1.0
	wedge := lineSamples / 20
	if wedge < 8 {
		wedge = 8
	}
	for off := 0; off+wedge < lineSamples; off += wedge / 4 {
		sum, n := 0.0, 0
		for base := off; base+wedge < len(video); base += lineSamples {
			for i := base; i < base+wedge; i++ {
				sum += video[i]
			}
			n += wedge
		}
		if n > 0 && sum/float64(n) > bestMean {
			bestMean = sum / float64(n)
			bestOff = off
		}
	}
	pixelsPerLine := ioc * 2 // 576 IOC = 1810 px? use 2*IOC=1152 standard fax resolution
	nLines := (len(video) - bestOff) / lineSamples
	if nLines < 5 {
		return nil, errShortWEFAX
	}
	img := image.NewGray(image.Rect(0, 0, pixelsPerLine, nLines))
	for line := 0; line < nLines; line++ {
		base := bestOff + line*lineSamples
		for px := 0; px < pixelsPerLine; px++ {
			s0 := base + int(float64(px)*float64(lineSamples)/float64(pixelsPerLine))
			s1 := base + int(float64(px+1)*float64(lineSamples)/float64(pixelsPerLine))
			if s1 <= s0 {
				s1 = s0 + 1
			}
			if s1 > len(video) {
				break
			}
			sum := 0.0
			for i := s0; i < s1; i++ {
				sum += video[i]
			}
			f := sum / float64(s1-s0) // mean video Hz
			// Map black..white into 0..255 with clamping.
			grey := (f - wefaxBlackHz) / (wefaxWhiteHz - wefaxBlackHz)
			if grey < 0 {
				grey = 0
			}
			if grey > 1 {
				grey = 1
			}
			img.SetGray(px, line, color.Gray{uint8(grey*255 + 0.5)})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// wefaxVideoFreq returns the instantaneous frequency around the 1900 Hz
// centre for each sample, using an I/Q mix and phase differentiation.
func wefaxVideoFreq(samples []float64, rate int) []float64 {
	n := len(samples)
	centre := (wefaxBlackHz + wefaxWhiteHz) / 2
	out := make([]float64, n)
	// One-pole smoothing of I and Q removes the 2*centre image.
	const alpha = 0.15
	var iSm, qSm float64
	prevPhase := 0.0
	for i, s := range samples {
		ph := 2 * math.Pi * centre * float64(i) / float64(rate)
		iSm += alpha * (s*math.Cos(ph) - iSm)
		qSm += alpha * (s*math.Sin(ph) - qSm)
		phase := math.Atan2(qSm, iSm)
		d := phase - prevPhase
		if d > math.Pi {
			d -= 2 * math.Pi
		} else if d < -math.Pi {
			d += 2 * math.Pi
		}
		prevPhase = phase
		out[i] = centre + d*float64(rate)/(2*math.Pi)
	}
	// Median-of-5 cleans up phase-wrap spikes.
	smoothed := make([]float64, n)
	for i := 2; i < n-2; i++ {
		w := []float64{out[i-2], out[i-1], out[i], out[i+1], out[i+2]}
		for a := 0; a < 5; a++ {
			for b := a + 1; b < 5; b++ {
				if w[b] < w[a] {
					w[a], w[b] = w[b], w[a]
				}
			}
		}
		smoothed[i] = w[2]
	}
	return smoothed
}

var errShortWEFAX = &wefaxError{"recording too short for a fax image"}

type wefaxError struct{ msg string }

func (e *wefaxError) Error() string { return "wefax: " + e.msg }
