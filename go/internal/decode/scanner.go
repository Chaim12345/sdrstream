package decode

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// BandScanner sweeps a receiver's dial across a frequency range, dwelling
// briefly at each step, and reports what each step heard: silence, a data
// burst, a CW carrier, or voice. It is deliberately coarse - a survey
// tool that tells you where to point a real recording - not a receiver.
type BandScanner struct {
	// Name identifies the receiver in reports.
	Name string
	// StepHz is the spacing between successive dial positions.
	StepHz float64
	// Dwell is how long to listen at each step.
	Dwell time.Duration

	// Tune moves the receiver to a frequency in Hz and mode.
	Tune func(hz float64, mode string) error
	// Capture records Dwell worth of audio and returns mono samples plus
	// the sample rate.
	Capture func() ([]float64, int, error)
}

// BandHit is one frequency at which the scanner found something.
type BandHit struct {
	FrequencyHz float64
	Mode        string
	Kind        string // silence | carrier | cw | data | voice
	LevelDB     float64
	ToneHz      float64
	SnrDB       float64
}

// ScanRange sweeps from startHz to endHz inclusive.
func (s *BandScanner) ScanRange(startHz, endHz, stepHz float64, modes []string) []BandHit {
	if stepHz <= 0 {
		stepHz = s.StepHz
	}
	if len(modes) == 0 {
		modes = []string{"fm"}
	}
	var hits []BandHit
	for hz := startHz; hz <= endHz; hz += stepHz {
		for _, mode := range modes {
			if err := s.Tune(hz, mode); err != nil {
				continue
			}
			samples, rate, err := s.Capture()
			if err != nil || len(samples) < rate/10 {
				continue
			}
			hit := Classify(samples, rate)
			hit.FrequencyHz = hz
			hit.Mode = mode
			if hit.Kind != "silence" {
				hits = append(hits, hit)
			}
		}
	}
	return hits
}

// classify estimates the nature of a captured slice. WebSDR audio is
// AGC-normalised, so absolute level says nothing; what distinguishes a
// real signal from boosted noise is spectral concentration. Spectral
// flatness (geometric mean over arithmetic mean of the power spectrum) is
// ~1 for white noise and near 0 for a carrier or data burst, and it is
// invariant to any gain the receiver applies.
func Classify(samples []float64, rate int) BandHit {
	hit := BandHit{Kind: "silence", LevelDB: -100}
	if len(samples) == 0 {
		return hit
	}
	var sum, peak float64
	for _, s := range samples {
		sum += s * s
		if a := math.Abs(s); a > peak {
			peak = a
		}
	}
	hit.LevelDB = 20 * math.Log10(math.Sqrt(sum/float64(len(samples)))+1e-9)

	flatness, peakTone, peakToFloor := spectrum(samples, rate)
	hit.ToneHz = peakTone
	hit.SnrDB = 10 * math.Log10(1/(flatness+1e-6))

	// Thresholds calibrated against real captures: WebSDR AGC boosts the
	// noise floor to about -27 dBFS, where spectral flatness sits at
	// 0.15-0.35. A real carrier or data signal drops it below 0.02, and
	// SSB/CW voice traffic lands near 0.001-0.01.
	switch {
	case peak < 0.02:
		hit.Kind = "silence"
	case flatness < 0.02 && peakToFloor > 12:
		// Highly concentrated: a carrier, keyed or data-modulated.
		if isOnOffKeyed(samples, rate, peakTone) {
			hit.Kind = "cw"
		} else {
			hit.Kind = "data"
		}
	case flatness < 0.10:
		hit.Kind = "voice"
	default:
		hit.Kind = "noise"
	}
	return hit
}

// spectrum returns the spectral flatness of the audio, the dominant tone,
// and the ratio of the strongest bin to the median bin. It evaluates the
// power spectrum on a coarse linear grid with Goertzel, which is enough
// for a survey and needs no FFT dependency.
func spectrum(samples []float64, rate int) (flatness, peakHz, peakToFloor float64) {
	const bins = 128
	hi := float64(rate) / 2
	powers := make([]float64, 0, bins)
	best, bestMag := 0.0, -1.0
	for i := 0; i < bins; i++ {
		f := (float64(i) + 1) * hi / float64(bins+1)
		mag := goertzel(samples, rate, f)
		powers = append(powers, mag)
		if mag > bestMag {
			bestMag, best = mag, f
		}
	}
	if bestMag <= 0 {
		return 1, 0, 0
	}
	var sumLog, sumLin float64
	sorted := append([]float64(nil), powers...)
	sort.Float64s(sorted)
	for _, p := range powers {
		sumLin += p
		sumLog += math.Log(p + 1e-18)
	}
	geo := math.Exp(sumLog / float64(bins))
	ari := sumLin / float64(bins)
	flat := geo / (ari + 1e-18)
	median := sorted[len(sorted)/2]
	return flat, best, bestMag / (median + 1e-18)
}

// OccupiedBandwidth measures how wide the signal in a recording really is,
// in Hz: the span between the weakest bins on either side of the strongest
// one that still hold most of the power. It is the measurement the narrow
// decoders need, because a channel width is not the same thing as a signal
// width: a receiver's AM filter passes 6 kHz, and an RTTY signal occupies
// 170 Hz of it. Without this the sweep path reports an AM voice call as
// RTTY text, because it has no bandwidth to compare against.
func OccupiedBandwidth(samples []float64, rate int) float64 {
	if len(samples) < 256 || rate <= 0 {
		return 0
	}
	// Restrict the search to the passband the decoders can use: below
	// 300 Hz is almost always AGC rumble, above 2.7 kHz nothing here
	// decodes.
	const low, high = 300.0, 2700.0
	const bins = 128
	binHz := (math.Min(high, float64(rate)/2) - low) / float64(bins)
	if binHz <= 0 {
		return 0
	}
	powers := make([]float64, bins)
	peak, peakPower := 0, 0.0
	for i := 0; i < bins; i++ {
		f := low + (float64(i)+0.5)*binHz
		powers[i] = goertzel(samples, rate, f)
		if powers[i] > peakPower {
			peak, peakPower = i, powers[i]
		}
	}
	if peakPower <= 0 {
		return 0
	}
	// Grow outward from the peak until 90% of the power inside the
	// passband is enclosed; the two boundaries are interpolated so the
	// result is finer than the bin grid.
	target := 0.9 * peakPower
	lowBin, highBin := float64(peak), float64(peak)
	i, j := peak, peak
	for i > 0 || j < bins-1 {
		below, above := 0.0, 0.0
		if i > 0 {
			below = powers[i-1]
		}
		if j < bins-1 {
			above = powers[j+1]
		}
		if below <= 0 && above <= 0 {
			break
		}
		if above >= below {
			j++
			if above < target {
				fraction := 0.0
				if above > 0 {
					fraction = (powers[j] - target) / powers[j]
				}
				highBin = float64(j) - fraction
			} else {
				highBin = float64(j)
			}
		} else {
			i--
			if below < target {
				fraction := 0.0
				if below > 0 {
					fraction = (target - powers[i]) / powers[i]
				}
				lowBin = float64(i) + fraction
			} else {
				lowBin = float64(i)
			}
		}
		if i == 0 && j == bins-1 {
			break
		}
	}
	width := (highBin - lowBin) * binHz
	if width < binHz {
		return binHz
	}
	return width
}

// Report renders hits as one line each, strongest first.
func Report(hits []BandHit) string {
	sorted := append([]BandHit(nil), hits...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].LevelDB > sorted[j].LevelDB })
	out := ""
	for _, h := range sorted {
		out += fmt.Sprintf("  %9.4f MHz  %-4s  %-8s  level %6.1f dB  tone %5.0f Hz  snr %4.0f dB\n",
			h.FrequencyHz/1e6, h.Mode, h.Kind, h.LevelDB, h.ToneHz, h.SnrDB)
	}
	return out
}
