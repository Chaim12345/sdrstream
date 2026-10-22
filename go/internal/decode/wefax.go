package decode

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"math"
	"sort"
)

// WEFAX demodulates HF weather fax (WMO F1C): audio subcarrier FM where
// 1500 Hz is black and 2300 Hz is white, 120 lines per minute (0.5 s per
// line) at IOC 576 (576*pi = 1810 px per line for DWD).
//
// The pipeline is built for noisy NVIS reception:
//
//  1. Tone-pair lock: the average spectrum between 1000 and 2800 Hz must
//     show a dominant pair of peaks near 1500/2300 Hz (accepting +-70 Hz
//     of transmitter/receiver offset). Without a locked pair the input is
//     rejected instead of rendering noise.
//  2. Envelope-ratio demodulation: per 2 ms hop the Goertzel power at
//     each locked tone is measured over an 8 ms window and brightness is
//     the ratio Pwhite/(Pwhite+Pblack). Fading moves both envelopes
//     together, so the ratio is stable where an amplitude or quadrature
//     discriminator is not.
//  3. Phasing wedge lock: the phasing block before the chart repeats a
//     white wedge in the first ~5% of each line. A sliding window finds
//     the block and the line-clock offset that maximises wedge contrast.
//  4. Per-line edge refinement with a flywheel: each phasing line's
//     wedge leading edge is re-measured to hop precision and a robust
//     straight line is fitted through the edges. The slope is the clock
//     drift (slant); the fitted clock is then flown through the whole
//     chart, which removes the curved stripes a fixed line length gives
//     on a recording whose sample clock differs from the transmitter's.
//
// WEFAXReport carries the diagnostics so callers can label results
// honestly instead of trusting a rendered PNG blindly.

const (
	wefaxBlackHz = 1500.0
	wefaxWhiteHz = 2300.0
	wefaxHopSec  = 0.002
	wefaxWinSec  = 0.008
)

// WEFAXReport describes how a WEFAX decode went.
type WEFAXReport struct {
	BlackHz         float64 // locked black tone (nominal 1500)
	WhiteHz         float64 // locked white tone (nominal 2300)
	PairConfidence  float64 // share of 1000-2800 Hz energy in the locked pair
	AntiCorrelation float64 // Pearson correlation of the two tone envelopes (FSK: negative)
	WedgeContrast   float64 // phasing wedge brightness minus line background (0..1)
	SyncSource      string  // "phasing-wedge" or "line-correlation"
	PeriodScore     float64 // line-correlation wrap alignment (best/mean column variance)
	PhasingStartSec float64 // where the phasing block was found
	PhasingLines    int     // phasing lines used for the clock fit
	SamplesPerLine  float64 // fitted line period including clock drift
	DriftPPM        float64 // (fitted/nominal - 1) * 1e6
	Lines           int     // rendered lines
	Width           int
	Height          int
}

var errShortWEFAX = &wefaxError{"recording too short for a fax image"}

type wefaxError struct{ msg string }

func (e *wefaxError) Error() string { return "wefax: " + e.msg }

// WEFAXImage demodulates samples (mono audio at rate) into a PNG image.
// linesPerMinute is 120 for DWD; zero uses 120. ioc is 576 for DWD; zero
// uses 576. It returns an error rather than a noise image when no fax
// signal is present.
func WEFAXImage(samples []float64, rate int, linesPerMinute float64, ioc int) ([]byte, error) {
	pngData, _, err := WEFAXImageDetailed(samples, rate, linesPerMinute, ioc)
	return pngData, err
}

// WEFAXImageDetailed is WEFAXImage plus a diagnostic report.
func WEFAXImageDetailed(samples []float64, rate int, linesPerMinute float64, ioc int) ([]byte, WEFAXReport, error) {
	var rep WEFAXReport
	if linesPerMinute <= 0 {
		linesPerMinute = 120
	}
	if ioc <= 0 {
		ioc = 576
	}
	if rate < 4000 {
		return nil, rep, fmt.Errorf("wefax: sample rate %d too low", rate)
	}
	lineSec := 60.0 / linesPerMinute
	lineSamples := lineSec * float64(rate)
	if len(samples) < int(lineSamples)*10 {
		return nil, rep, errShortWEFAX
	}

	black, white, share, prominence := wefaxLockTonePair(samples, rate)
	rep.BlackHz, rep.WhiteHz, rep.PairConfidence = black, white, share
	if prominence < 2.5 || share < 0.05 {
		return nil, rep, fmt.Errorf("wefax: no 1500/2300 Hz tone pair locked (peak prominence %.1fx, pair energy share %.2g)", prominence, share)
	}

	tr := wefaxDemodRatio(samples, rate, black, white)
	// A steady carrier leaks equally into both tone bins: the envelopes
	// move together (correlation near +1). Real FSK keys one bin at a
	// time (negative correlation).
	if ac := tr.antiCorrelation(0, len(tr.ratio)); ac > 0.6 {
		return nil, rep, fmt.Errorf("wefax: tone envelopes correlate at %.2f (carrier, not FSK)", ac)
	}

	lineHops := int(math.Round(lineSec / wefaxHopSec))
	if lineHops < 20 {
		return nil, rep, fmt.Errorf("wefax: line period %.3f s too short for hop grid", lineSec)
	}
	var a, b float64
	startLine, off, contrast, ok := tr.findPhasing(lineHops)
	rep.WedgeContrast = contrast
	synced := false
	if ok {
		// Phasing block present: absolute line phase plus a per-line
		// wedge-edge clock fit. Chart content can fake a weak wedge,
		// so a fit that finds no stable edges falls through to the
		// correlation path.
		var used int
		a, b, used = tr.fitLineClock(startLine, 60, off, lineHops)
		rep.PhasingLines = used
		if used >= 8 {
			synced = true
			rep.SyncSource = "phasing-wedge"
			rep.PhasingStartSec = float64(startLine*lineHops) * wefaxHopSec
			rep.AntiCorrelation = tr.antiCorrelation(startLine*lineHops, 60*lineHops)
		}
	}
	if !synced {
		// Mid-chart capture with no phasing block: recover the exact
		// line period from the wrap that maximises column variance
		// (chart structure correlates line to line). The horizontal
		// phase is unknown, so the chart may be wrapped left/right.
		rep.SyncSource = "line-correlation"
		rep.AntiCorrelation = tr.antiCorrelation(0, len(tr.ratio))
		per, score := tr.fitPeriodByWrap(lineHops)
		rep.PeriodScore = score
		if score < 0.20 {
			return nil, rep, fmt.Errorf("wefax: no phasing wedge (contrast %.2f, %d stable edges) and no line-period lock (score %.2f)", contrast, rep.PhasingLines, score)
		}
		a, b, startLine = 0, per, 0
	}
	rep.SamplesPerLine = b * wefaxHopSec * float64(rate)
	rep.DriftPPM = (b/float64(lineHops) - 1) * 1e6

	ppl := int(math.Round(float64(ioc) * math.Pi))
	img, lines := tr.render(a, b, startLine, lineHops, ppl)
	if lines < 5 {
		return nil, rep, errShortWEFAX
	}
	rep.Lines, rep.Width, rep.Height = lines, ppl, lines

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, rep, err
	}
	return buf.Bytes(), rep, nil
}

// wefaxLockTonePair finds the dominant black/white tone pair in the
// average spectrum. It returns the locked frequencies, the share of
// wideband energy they carry, and the prominence of the weaker peak over
// the band median. Prominence is the honest gate: a chart that is mostly
// white keys the black bin rarely, so its share of total energy stays
// small even on a strong signal, while both peaks still tower over the
// noise floor. A bare carrier or plain noise has no prominent pair.
func wefaxLockTonePair(samples []float64, rate int) (black, white, share, prominence float64) {
	winN := int(0.5 * float64(rate))
	if winN < 1024 {
		winN = 1024
	}
	nWin := 16
	if len(samples) < winN*4 {
		nWin = len(samples) / winN
		if nWin < 1 {
			return 0, 0, 0, 0
		}
	}
	step := (len(samples) - winN) / nWin
	if step < 1 {
		step = 1
	}
	// Average power on a 10 Hz grid over 1000-2800 Hz.
	const f0, f1, df = 1000.0, 2800.0, 10.0
	nBins := int((f1-f0)/df) + 1
	power := make([]float64, nBins)
	for w := 0; w < nWin; w++ {
		seg := samples[w*step : w*step+winN]
		for b := 0; b < nBins; b++ {
			power[b] += goertzel(seg, rate, f0+float64(b)*df)
		}
	}
	total := 0.0
	for _, p := range power {
		total += p
	}
	if total <= 0 {
		return 0, 0, 0, 0
	}
	median := power[len(power)/2]
	{
		sorted := make([]float64, len(power))
		copy(sorted, power)
		sort.Float64s(sorted)
		median = sorted[len(sorted)/2]
	}
	peakIn := func(lo, hi float64) (freq, p float64) {
		// Integrate 3 bins around each grid point to catch splatter.
		for i := 1; i+1 < nBins; i++ {
			f := f0 + float64(i)*df
			if f < lo || f > hi {
				continue
			}
			v := power[i-1] + power[i] + power[i+1]
			if v > p {
				p, freq = v, f
			}
		}
		return
	}
	black, pb := peakIn(wefaxBlackHz-150, wefaxBlackHz+150)
	white, pw := peakIn(wefaxWhiteHz-150, wefaxWhiteHz+150)
	if white-black < 600 || white-black > 1000 || median <= 0 {
		return black, white, 0, 0
	}
	prom := math.Min(pb, pw) / median
	return black, white, (pb + pw) / total, prom
}

// wefaxTrace is the envelope-ratio demodulation of a capture.
type wefaxTrace struct {
	ratio []float64 // brightness 0 (black) .. 1 (white) per hop
	pb    []float64 // black tone power per hop
	pw    []float64 // white tone power per hop
}

// wefaxDemodRatio computes per-hop Goertzel powers at the two locked
// tones and their brightness ratio, median-3 smoothed.
func wefaxDemodRatio(samples []float64, rate int, blackHz, whiteHz float64) *wefaxTrace {
	win := int(wefaxWinSec * float64(rate))
	hop := int(wefaxHopSec * float64(rate))
	if win < 8 {
		win = 8
	}
	if hop < 1 {
		hop = 1
	}
	n := (len(samples) - win) / hop
	if n <= 0 {
		return &wefaxTrace{}
	}
	t := &wefaxTrace{
		ratio: make([]float64, n),
		pb:    make([]float64, n),
		pw:    make([]float64, n),
	}
	for h := 0; h < n; h++ {
		seg := samples[h*hop : h*hop+win]
		pb := goertzel(seg, rate, blackHz)
		pw := goertzel(seg, rate, whiteHz)
		t.pb[h], t.pw[h] = pb, pw
		t.ratio[h] = pw / (pw + pb + 1e-12)
	}
	// Pedestal subtraction: a steady co-channel carrier adds the same
	// pedestal to one tone's bin whether the signal keys or fades. The
	// rolling minimum over ~0.5 s estimates that pedestal per bin, and
	// subtracting it leaves only the keyed tone's excess. Recompute the
	// ratio from the pedestal-free envelopes.
	win2 := int(0.5 / wefaxHopSec)
	if win2 < 8 {
		win2 = 8
	}
	pedB := rollingMin(t.pb, win2)
	pedW := rollingMin(t.pw, win2)
	for h := 0; h < n; h++ {
		b := t.pb[h] - pedB[h]
		w := t.pw[h] - pedW[h]
		if b < 0 {
			b = 0
		}
		if w < 0 {
			w = 0
		}
		t.ratio[h] = w / (w + b + 1e-12)
	}
	// A carrier sitting on one of the tone frequencies beats with the
	// keyed tone and writes a stable sinusoid into the ratio (47.9 Hz
	// observed on DWD 3855 via a Bonn KiwiSDR, 2026-10-22). Wrapped at
	// the line period it renders as dense diagonal striping that hides
	// the chart. Detect a strong narrow beat line and subtract it and
	// its harmonics before any sync logic runs.
	t.removeBeat()
	// Median-3 cleans up single-hop spikes without smearing edges.
	for h := 1; h+1 < n; h++ {
		a, b, c := t.ratio[h-1], t.ratio[h], t.ratio[h+1]
		if a > b {
			a, b = b, a
		}
		if b > c {
			b, c = c, b
		}
		if a > b {
			a, b = b, a
		}
		t.ratio[h] = b
	}
	return t
}

// rollingMin returns the per-index minimum over a centred window of
// width w, using a two-pass block decomposition.
func rollingMin(v []float64, w int) []float64 {
	n := len(v)
	out := make([]float64, n)
	pre := make([]float64, n)
	suf := make([]float64, n)
	for i := 0; i < n; i++ {
		if i%w == 0 {
			pre[i] = v[i]
		} else {
			pre[i] = math.Min(pre[i-1], v[i])
		}
	}
	for i := n - 1; i >= 0; i-- {
		if i == n-1 || (i+1)%w == 0 {
			suf[i] = v[i]
		} else {
			suf[i] = math.Min(suf[i+1], v[i])
		}
	}
	half := w / 2
	for i := 0; i < n; i++ {
		lo, hi := i-half, i+half
		m := math.Inf(1)
		if lo >= 0 {
			m = suf[lo]
		}
		if hi < n {
			m = math.Min(m, pre[hi])
		}
		if math.IsInf(m, 1) {
			m = v[i]
		}
		out[i] = m
	}
	return out
}

// removeBeat detects a strong narrow spectral line in the 15-60 Hz range
// of the ratio trace (a co-channel carrier beating with a keyed tone)
// and subtracts it and its first two harmonics by least squares. Weak or
// absent beats leave the trace untouched.
func (t *wefaxTrace) removeBeat() {
	n := len(t.ratio)
	if n < 2000 {
		return
	}
	hopRate := 1.0 / wefaxHopSec
	powerAt := func(freq float64) float64 {
		k := 2 * math.Pi * freq / hopRate
		var wr, wi float64
		for h := 0; h < n; h++ {
			wr += t.ratio[h] * math.Cos(k*float64(h))
			wi += t.ratio[h] * math.Sin(k*float64(h))
		}
		return (wr*wr + wi*wi) / float64(n)
	}
	// Coarse scan 15-60 Hz at 0.5 Hz steps.
	best, bestP := 0.0, 0.0
	var vals []float64
	for f := 15.0; f <= 60.0; f += 0.5 {
		p := powerAt(f)
		vals = append(vals, p)
		if p > bestP {
			bestP, best = p, f
		}
	}
	sorted := make([]float64, len(vals))
	copy(sorted, vals)
	sort.Float64s(sorted)
	median := sorted[len(sorted)/2]
	if median <= 0 || bestP/median < 6 {
		return
	}
	// Fine scan around the winner at 0.05 Hz.
	f0 := best
	for f := best - 0.5; f <= best+0.5; f += 0.05 {
		if p := powerAt(f); p > bestP {
			bestP, f0 = p, f
		}
	}
	// Project out the fundamental and harmonics sequentially; over a
	// long capture the harmonics are near-orthogonal.
	for h := 1; h <= 3; h++ {
		f := f0 * float64(h)
		if f > hopRate/2 {
			break
		}
		k := 2 * math.Pi * f / hopRate
		var a, b float64
		for i := 0; i < n; i++ {
			a += t.ratio[i] * math.Cos(k*float64(i))
			b += t.ratio[i] * math.Sin(k*float64(i))
		}
		a *= 2.0 / float64(n)
		b *= 2.0 / float64(n)
		for i := 0; i < n; i++ {
			t.ratio[i] -= a*math.Cos(k*float64(i)) + b*math.Sin(k*float64(i))
		}
	}
}

// antiCorrelation returns the Pearson correlation of the two tone
// envelope traces over hops [from, from+count): strongly negative for
// real FSK keying, near zero for noise or an unmodulated carrier.
func (t *wefaxTrace) antiCorrelation(from, count int) float64 {
	to := from + count
	if to > len(t.pb) {
		to = len(t.pb)
	}
	if from < 0 || to-from < 100 {
		return 0
	}
	n := float64(to - from)
	var sb, sw, sbb, sww, sbw float64
	for h := from; h < to; h++ {
		sb += t.pb[h]
		sw += t.pw[h]
		sbb += t.pb[h] * t.pb[h]
		sww += t.pw[h] * t.pw[h]
		sbw += t.pb[h] * t.pw[h]
	}
	num := sbw/n - (sb/n)*(sw/n)
	den := math.Sqrt((sbb/n - (sb/n)*(sb/n)) * (sww/n - (sw/n)*(sw/n)))
	if den <= 0 {
		return 0
	}
	return num / den
}

// wedgeMean averages the ratio trace over hops [from, to).
func (t *wefaxTrace) wedgeMean(from, to int) float64 {
	if from < 0 {
		from = 0
	}
	if to > len(t.ratio) {
		to = len(t.ratio)
	}
	if to-from < 1 {
		return 0.5
	}
	sum := 0.0
	for h := from; h < to; h++ {
		sum += t.ratio[h]
	}
	return sum / float64(to-from)
}

// findPhasing locates the phasing block: the run of lines whose leading
// white wedge gives the strongest periodic contrast. It returns the
// block's first line index, the line-clock offset in hops, and the wedge
// contrast achieved there.
func (t *wefaxTrace) findPhasing(lineHops int) (startLine int, offset float64, contrast float64, ok bool) {
	wedge := lineHops / 20
	if wedge < 4 {
		wedge = 4
	}
	nLines := len(t.ratio) / lineHops
	if nLines < 10 {
		return 0, 0, 0, false
	}
	const winLines = 60
	score := func(off, s int) float64 {
		acc, n := 0.0, 0
		for l := s; l < s+winLines && l < nLines; l++ {
			base := off + l*lineHops
			white := t.wedgeMean(base, base+wedge)
			rest := t.wedgeMean(base+wedge, base+lineHops)
			acc += white - rest
			n++
		}
		if n == 0 {
			return -1
		}
		return acc / float64(n)
	}
	best, bestS, bestO := -1.0, 0, 0
	stepLines := 15
	if nLines < winLines+stepLines {
		stepLines = 1
	}
	for s := 0; s+10 <= nLines; s += stepLines {
		for o := 0; o+wedge < lineHops; o += 2 {
			if v := score(o, s); v > best {
				best, bestS, bestO = v, s, o
			}
		}
	}
	// Refine the offset to single hops around the coarse winner.
	for o := bestO - 2; o <= bestO+2; o++ {
		if o < 0 || o+wedge >= lineHops {
			continue
		}
		if v := score(o, bestS); v > best {
			best, bestO = v, o
		}
	}
	if best < 0.08 {
		return bestS, float64(bestO), best, false
	}
	return bestS, float64(bestO), best, true
}

// fitLineClock re-measures each phasing line's wedge leading edge to hop
// precision and fits edge(line) = a + b*line by least squares with
// outlier rejection. b is the true line period in hops, so its deviation
// from the nominal grid is the sample-clock drift that slants the image.
func (t *wefaxTrace) fitLineClock(startLine, nLines int, off float64, lineHops int) (a, b float64, used int) {
	wedge := lineHops / 20
	if wedge < 4 {
		wedge = 4
	}
	nAvail := len(t.ratio) / lineHops
	// Two passes: the first measures edges against the nominal grid,
	// the second chases the fitted clock, so drift larger than the
	// search window across the block is still tracked.
	predict := func(l int) float64 { return off + float64(l*lineHops) }
	var pts []wedgePt
	for pass := 0; pass < 2; pass++ {
		pts = pts[:0]
		for l := startLine; l < startLine+nLines && l < nAvail; l++ {
			exp := predict(l)
			bestScore, bestP := 0.10, -1.0
			from := int(exp) - 6
			to := int(exp) + 6
			for p := from; p <= to; p++ {
				c := t.wedgeMean(p, p+wedge) - t.wedgeMean(p-wedge, p)
				if c > bestScore {
					bestScore, bestP = c, float64(p)
				}
			}
			if bestP >= 0 {
				pts = append(pts, wedgePt{l, bestP})
			}
		}
		if len(pts) < 4 {
			return off, float64(lineHops), len(pts)
		}
		a0, b0 := fitLineClockLS(pts, float64(lineHops))
		predict = func(l int) float64 { return a0 + b0*float64(l) }
		if pass == 1 {
			return fitLineClockRefined(pts, float64(lineHops))
		}
	}
	return off, float64(lineHops), 0
}

type wedgePt struct {
	l int
	p float64
}

func fitLineClockLS(pts []wedgePt, nominal float64) (a, b float64) {
	var sl, sp, sll, slp float64
	for _, q := range pts {
		l := float64(q.l)
		sl += l
		sp += q.p
		sll += l * l
		slp += l * q.p
	}
	n := float64(len(pts))
	den := sll - sl*sl/n
	if den == 0 {
		return sp / n, nominal
	}
	b = (slp - sl*sp/n) / den
	a = (sp - b*sl) / n
	return a, b
}

// fitLineClockRefined runs least squares with two rounds of 2.5-sigma
// outlier rejection.
func fitLineClockRefined(pts []wedgePt, nominal float64) (a, b float64, used int) {
	a, b = fitLineClockLS(pts, nominal)
	for round := 0; round < 2; round++ {
		var ss float64
		for _, q := range pts {
			d := q.p - (a + b*float64(q.l))
			ss += d * d
		}
		sigma := math.Sqrt(ss / float64(len(pts)))
		if sigma < 0.5 {
			sigma = 0.5
		}
		kept := pts[:0]
		for _, q := range pts {
			if d := q.p - (a + b*float64(q.l)); d*d <= 6.25*sigma*sigma {
				kept = append(kept, q)
			}
		}
		if len(kept) < 4 {
			break
		}
		pts = kept
		a, b = fitLineClockLS(pts, nominal)
	}
	return a, b, len(pts)
}

// fitPeriodByWrap finds the true line period in hops by wrapping the
// ratio trace at candidate periods and maximising the mean per-column
// standard deviation: chart structure repeats line to line, so the wrap
// aligned to the true period stacks features into columns. A two-stage
// coarse/fine scan reaches 0.02-hop (10 us) resolution, which keeps the
// slant under a pixel over a full chart. The score returned is the best
// column variance divided by the mean over the scan, so an aligned wrap
// stands out from a featureless one.
func (t *wefaxTrace) fitPeriodByWrap(lineHops int) (per, score float64) {
	// Drift estimate (idea from helper C's rewrite, made fractional):
	// every consecutive line pair votes for the shift that maximises
	// their correlation; a parabola through the peak's neighbours gives
	// sub-hop resolution, and the median over hundreds of pairs rejects
	// the scatter that noise and sloped chart content add.
	var shifts []float64
	nL := len(t.ratio) / lineHops
	corrAt := func(l, sh int) float64 {
		base := l * lineHops
		var sab, saa, sbb float64
		for i := 8; i < lineHops-8; i++ {
			j := base + lineHops + i + sh
			if j < 0 || j >= len(t.ratio) {
				return -1
			}
			a := t.ratio[base+i]
			b := t.ratio[j]
			sab += a * b
			saa += a * a
			sbb += b * b
		}
		if saa <= 0 || sbb <= 0 {
			return -1
		}
		return sab / math.Sqrt(saa*sbb)
	}
	for l := 0; l+1 < nL; l++ {
		bestSh, bestC := 0, -2.0
		cs := make([]float64, 5)
		for sh := -2; sh <= 2; sh++ {
			c := corrAt(l, sh)
			cs[sh+2] = c
			if c > bestC {
				bestC, bestSh = c, sh
			}
		}
		frac := 0.0
		if bestSh > -2 && bestSh < 2 {
			cm, c0, cp := cs[bestSh+1], cs[bestSh+2], cs[bestSh+3]
			if den := cm - 2*c0 + cp; den < 0 {
				frac = 0.5 * (cm - cp) / den
			}
		}
		shifts = append(shifts, float64(bestSh)+frac)
	}
	drift := 0.0
	if len(shifts) >= 8 {
		sort.Float64s(shifts)
		drift = shifts[len(shifts)/2]
	}
	coarse := float64(lineHops) + drift
	// The gate is the mean zero-lag correlation of consecutive lines at
	// the fitted period: chart structure persists over several lines,
	// noise and interference do not. On the Bonn 3855 kHz capture this
	// is ~0.01 (no chart recoverable); a clean chart fixture is > 0.3.
	fineBest := coarse
	nLines := int(float64(len(t.ratio)) / fineBest)
	cols := int(fineBest)
	var tot float64
	var pairs int
	prev := make([]float64, cols)
	for l := 0; l < nLines; l++ {
		row := make([]float64, cols)
		base := float64(l) * fineBest
		for c := 0; c < cols; c++ {
			row[c] = t.ratioAt(base + float64(c))
		}
		if l > 0 {
			// Zero-mean the rows: near-constant rows (a faded or
			// carrier-only capture after pedestal subtraction) must
			// not correlate, only shared structure may.
			var m1, m2 float64
			for c := 0; c < cols; c++ {
				m1 += prev[c]
				m2 += row[c]
			}
			m1 /= float64(cols)
			m2 /= float64(cols)
			var sab, saa, sbb float64
			for c := 0; c < cols; c++ {
				a1 := prev[c] - m1
				b1 := row[c] - m2
				sab += a1 * b1
				saa += a1 * a1
				sbb += b1 * b1
			}
			if saa > 1e-12 && sbb > 1e-12 {
				tot += sab / math.Sqrt(saa*sbb)
				pairs++
			}
		}
		prev, row = row, prev
	}
	if pairs == 0 {
		return fineBest, 0
	}
	return fineBest, tot / float64(pairs)
}

// ratioAt linearly interpolates the ratio trace at a fractional hop.
func (t *wefaxTrace) ratioAt(h float64) float64 {
	if h < 0 {
		return t.ratio[0]
	}
	i := int(h)
	if i+1 >= len(t.ratio) {
		return t.ratio[len(t.ratio)-1]
	}
	f := h - float64(i)
	return t.ratio[i]*(1-f) + t.ratio[i+1]*f
}

// render produces the greyscale image flying the fitted clock a + b*l
// from absolute line startLine to the end of the trace, and normalises
// contrast to the 2nd/98th percentile of the rendered brightness.
func (t *wefaxTrace) render(a, b float64, startLine, lineHops, ppl int) (*image.Gray, int) {
	lastLine := int((float64(len(t.ratio)-1) - a) / b)
	if lastLine <= startLine {
		return nil, 0
	}
	nLines := lastLine - startLine
	raw := make([]float64, nLines*ppl)
	step := b / float64(ppl) // hops per pixel
	subs := int(step) + 1
	if subs < 2 {
		subs = 2
	}
	for y := 0; y < nLines; y++ {
		start := a + b*float64(startLine+y)
		for x := 0; x < ppl; x++ {
			lo := start + float64(x)*step
			sum := 0.0
			for s2 := 0; s2 < subs; s2++ {
				sum += t.ratioAt(lo + step*float64(s2)/float64(subs))
			}
			raw[y*ppl+x] = sum / float64(subs)
		}
	}
	// Percentile contrast stretch.
	sorted := make([]float64, len(raw))
	copy(sorted, raw)
	sort.Float64s(sorted)
	lo := sorted[int(0.02*float64(len(sorted)-1))]
	hi := sorted[int(0.98*float64(len(sorted)-1))]
	if hi-lo < 0.05 {
		hi = lo + 0.05
	}
	img := image.NewGray(image.Rect(0, 0, ppl, nLines))
	for i, v := range raw {
		g := (v - lo) / (hi - lo)
		if g < 0 {
			g = 0
		}
		if g > 1 {
			g = 1
		}
		img.Pix[i] = uint8(g*255 + 0.5)
	}
	return img, nLines
}
