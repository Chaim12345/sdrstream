// Package wefaxfixture synthesizes F1C (WEFAX) transmissions for
// decoder and publication-path tests. Test-only support: nothing here
// is a production code path, and the signals are synthetic - they must
// never be presented as received content.
package wefaxfixture

import (
	"math"
	"math/rand"
)

// Fixture describes a synthesized F1C transmission.
type Fixture struct {
	Rate         float64
	PhasingLines int
	ImageLines   int
	BlackHz      float64 // 0 -> 1500
	WhiteHz      float64 // 0 -> 2300
	LineSec      float64 // 0 -> 0.5 (set off-nominal to simulate clock drift)
	NoiseAmp     float64 // additive white noise amplitude
	StartToneSec float64 // 300 Hz IOC-576 start tone before phasing
	Bars         bool    // image content: horizontal bars (mid-chart captures)
	Gradient     bool    // image content: vertical gray-scale bands (tone fidelity)
	GradBands    int     // number of gradient bands; 0 -> 6
	FlatGradient bool    // LIMITATION fixture: flat bands, no per-line structure
	BarGradient  bool    // LIMITATION fixture: gray bars reaching the right edge
	CarrierHz    float64 // steady carrier near the white tone (beat test)
	CarrierAmp   float64
}

// Encode synthesizes F1C audio: an optional start tone, a phasing
// block (black lines with a 5% white leading wedge), then image lines
// with a diagonal test pattern plus a checkerboard band.
func Encode(f Fixture) []float64 {
	if f.Rate == 0 {
		f.Rate = 12000
	}
	if f.BlackHz == 0 {
		f.BlackHz = 1500
	}
	if f.WhiteHz == 0 {
		f.WhiteHz = 2300
	}
	if f.LineSec == 0 {
		f.LineSec = 0.5
	}
	lineSamples := int(f.LineSec * f.Rate)
	n := int(f.StartToneSec*f.Rate) + (f.PhasingLines+f.ImageLines)*lineSamples
	out := make([]float64, n)
	rng := rand.New(rand.NewSource(42))
	phase := 0.0
	startToneN := int(f.StartToneSec * f.Rate)
	for i := 0; i < n; i++ {
		freq := 300.0
		if i >= startToneN {
			k := i - startToneN
			line := k / lineSamples
			inLine := k % lineSamples
			frac := float64(inLine) / float64(lineSamples)
			grey := 0.0
			switch {
			case line < f.PhasingLines:
				if frac < 0.05 {
					grey = 1.0 // phasing wedge
				}
			default:
				il := line - f.PhasingLines
				px := frac
				if f.Bars {
					// Horizontal bars 3 lines tall every 7 lines at
					// varying x extents: chart-like content with no
					// periodic white wedge.
					right := 0.8
					if f.BarGradient {
						// LIMITATION fixture: light content reaching
						// the right edge mimics a phasing wedge.
						right = 0.98
					}
					if il%7 < 3 && px > 0.1+0.3*math.Abs(math.Sin(float64(il/7))) && px < right {
						grey = 1.0
						if f.BarGradient {
							nb := f.GradBands
							if nb == 0 {
								nb = 4
							}
							band := int(px * float64(nb))
							if band >= nb {
								band = nb - 1
							}
							grey = float64(band) / float64(nb-1)
						}
					}
				} else if f.FlatGradient {
					// LIMITATION fixture: flat vertical bands give the
					// tracker no per-line structure; plateau content
					// biases the local clock votes.
					nb := f.GradBands
					if nb == 0 {
						nb = 6
					}
					band := int(px * float64(nb))
					if band >= nb {
						band = nb - 1
					}
					grey = float64(band) / float64(nb-1)
				} else if f.Gradient {
					// Checkerboard cells take the x-band gray level
					// instead of white: the proven tracker-friendly
					// structure exercises intermediate tones, not
					// just the bilevel endpoints. Flat band content
					// alone biases the local clock votes (plateaus).
					nb := f.GradBands
					if nb == 0 {
						nb = 6
					}
					if (int(px*20)+il)%2 == 0 {
						band := int(px * float64(nb))
						if band >= nb {
							band = nb - 1
						}
						grey = float64(band) / float64(nb-1)
					}
				} else if il < f.ImageLines/2 {
					// Diagonal stripe over the upper half, checkerboard below.
					if math.Abs(px-float64(il)/float64(f.ImageLines/2)) < 0.08 {
						grey = 1.0
					}
				} else {
					if (int(px*20)+il)%2 == 0 {
						grey = 1.0
					}
				}
			}
			freq = f.BlackHz + grey*(f.WhiteHz-f.BlackHz)
		}
		phase += 2 * math.Pi * freq / f.Rate
		out[i] = 0.6*math.Sin(phase) + f.NoiseAmp*(rng.Float64()*2-1)
	}
	if f.CarrierHz > 0 {
		for i := range out {
			out[i] += f.CarrierAmp * math.Sin(2*math.Pi*f.CarrierHz*float64(i)/f.Rate)
		}
	}
	return out
}
