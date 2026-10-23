package decode

import (
	"image"
	"math"
	"sort"
)

// wefax_sync.go: start-tone detection, IOC selection and the
// time-varying line clock (slant correction) for WEFAX.
//
// The clock problem: real recordings (amateur VHF demos, tape airchecks,
// long KiwiSDR captures) do not have a constant sample-clock offset.
// The archive.org IOC576 demo wanders between -3700 ppm and +700 ppm
// over ten minutes, so no single "samples per line" deskews it: the
// chart slants no matter what constant is fitted. The fix here tracks
// the line clock continuously: consecutive chart lines carry almost the
// same content, so each line pair votes for the small shift that best
// aligns them; a median filter over the votes gives the local period
// error, and integrating those periods yields a per-line clock curve.

// wefaxClock is the fitted time-varying line clock over a trace.
type wefaxClock struct {
	start []float64 // hop offset where each line starts
	per   []float64 // each line's period in hops
}

// findStartTone locates the IOC start alternation: before the phasing
// block a WEFAX transmitter keys between the black and white tones at
// 300 Hz (IOC 576) or 675 Hz (IOC 288). It works on the raw audio with
// 1 ms tone-envelope windows: the decoder's 8 ms demod window spans
// several alternation cycles and averages the keying away. Returns the
// tone's start/end in seconds and the detected IOC.
func (t *wefaxTrace) findStartTone(samples []float64, rate int, blackHz, whiteHz float64) (startSec, endSec float64, ioc int, ok bool) {
	win := int(0.001 * float64(rate)) // 1 ms envelope window
	hop := win / 2
	if win < 4 || hop < 2 {
		return 0, 0, 0, false
	}
	limit := len(samples) - win
	maxN := int(180.0 * float64(rate)) // start tones live up front
	if limit > maxN {
		limit = maxN
	}
	if limit < 4*rate {
		return 0, 0, 0, false
	}
	n := limit / hop
	d := make([]float64, n)
	for h := 0; h < n; h++ {
		seg := samples[h*hop : h*hop+win]
		d[h] = goertzel(seg, rate, whiteHz) - goertzel(seg, rate, blackHz)
	}
	// Absolute energy floor: a keyed tone swings d at full power. A
	// constant audio tone (or silence) leaks into both bins equally
	// and must not qualify, however its normalized share looks.
	sortedD := make([]float64, n)
	for h := range d {
		sortedD[h] = d[h] * d[h]
	}
	sort.Float64s(sortedD)
	fullEnergy := sortedD[n/2]
	hopSec := float64(hop) / float64(rate)
	// Per 2 s window, d is a clean 300/675 Hz square wave during the
	// start tone and slow content during the chart: the gate is the
	// keying band's share of the window energy.
	wspan := int(2.0 / hopSec)
	wstep := wspan / 4
	type hit struct{ from, to int }
	var runs []hit
	runStart := -1
	share := func(w0 int) (float64, float64, float64) {
		var tot, p3, p6 float64
		k3 := 2 * math.Pi * 300 * hopSec
		k6 := 2 * math.Pi * 675 * hopSec
		var r3, i3, r6, i6 float64
		for h := w0; h < w0+wspan && h < n; h++ {
			tot += d[h] * d[h]
			r3 += d[h] * math.Cos(k3*float64(h))
			i3 += d[h] * math.Sin(k3*float64(h))
			r6 += d[h] * math.Cos(k6*float64(h))
			i6 += d[h] * math.Sin(k6*float64(h))
		}
		if tot <= 0 || tot/float64(wspan) < 0.25*fullEnergy {
			return 0, 0, 0
		}
		p3 = 2 * (r3*r3 + i3*i3) / (tot * float64(wspan))
		p6 = 2 * (r6*r6 + i6*i6) / (tot * float64(wspan))
		s := p3
		if p6 > s {
			s = p6
		}
		return s, p3, p6
	}
	for w0 := 0; w0+wspan <= n; w0 += wstep {
		s, _, _ := share(w0)
		if s > 0.15 {
			if runStart < 0 {
				runStart = w0
			}
		} else if runStart >= 0 {
			runs = append(runs, hit{runStart, w0 + wspan})
			runStart = -1
		}
	}
	if runStart >= 0 {
		runs = append(runs, hit{runStart, n})
	}
	var best hit
	found := false
	for _, r := range runs {
		if float64(r.to-r.from)*hopSec >= 1.5 && (!found || r.from < best.from) {
			best, found = r, true
		}
	}
	if !found {
		return 0, 0, 0, false
	}
	_, p3, p6 := share(best.from)
	ioc = 288
	if p3 >= p6 {
		ioc = 576
	}
	return float64(best.from) * hopSec, float64(best.to) * hopSec, ioc, true
}

// corrShifted returns the zero-mean Pearson correlation between the
// trace segment starting at hop a and the (fractionally shifted)
// segment starting at hop b, over n hops. Alignment runs on the raw
// (pre-pedestal) ratio, which preserves line-scale structure.
func (t *wefaxTrace) corrShifted(a, b float64, n int) float64 {
	tr := t.raw
	if tr == nil {
		tr = t.ratio
	}
	_ = tr
	if a < 0 || b < 0 || int(a)+n >= len(tr) || int(b)+n >= len(tr) {
		return -1
	}
	at := func(h float64) float64 {
		if h < 0 {
			return tr[0]
		}
		i := int(h)
		if i+1 >= len(tr) {
			return tr[len(tr)-1]
		}
		f := h - float64(i)
		return tr[i]*(1-f) + tr[i+1]*f
	}
	var m1, m2 float64
	for i := 0; i < n; i++ {
		m1 += at(a + float64(i))
		m2 += at(b + float64(i))
	}
	m1 /= float64(n)
	m2 /= float64(n)
	var sab, saa, sbb float64
	for i := 0; i < n; i++ {
		x := at(a+float64(i)) - m1
		y := at(b+float64(i)) - m2
		sab += x * y
		saa += x * x
		sbb += y * y
	}
	if saa < 1e-9 || sbb < 1e-9 {
		return -1
	}
	return sab / math.Sqrt(saa*sbb)
}

// trackLineClock fits the time-varying line clock. anchorLine is a line
// whose absolute start (anchorStart hops) is known - the phasing block
// when present, else the trace start. Each pass lets every consecutive
// line pair vote for the shift (-3..+3 hops, parabolic sub-hop) that
// best aligns their content, median-filters the votes over +-15 lines,
// and integrates the corrected periods through the anchor. Recorder
// wander of several thousand ppm is absorbed locally, so a slanted
// capture renders straight.
func (t *wefaxTrace) trackLineClock(lineHops, anchorLine int, anchorStart, anchorPer float64, passes int) (clk *wefaxClock, votes, medPPM, minPPM, maxPPM float64) {
	nLines := len(t.ratio) / lineHops
	if nLines < 3 {
		return &wefaxClock{}, 0, 0, 0, 0
	}
	if anchorLine < 0 || anchorLine >= nLines {
		anchorLine = 0
		anchorStart = 0
	}
	if anchorPer <= 0 {
		anchorPer = float64(lineHops)
	}
	clk = &wefaxClock{start: make([]float64, nLines), per: make([]float64, nLines)}
	for l := 0; l < nLines; l++ {
		clk.start[l] = anchorStart + float64(l-anchorLine)*anchorPer
		clk.per[l] = anchorPer
	}
	seg := lineHops - 16
	if seg < 20 {
		seg = lineHops / 2
	}
	// Vote rule: scan wide (+-8 hops) so chart features that genuinely
	// move - a diagonal coastline shifts several hops per line - are
	// SEEN and rejected, not clipped into fake clock votes. Only pairs
	// aligning within +-2.5 hops of the current clock vote: the true
	// per-line clock error of any real recorder is under a hop, so a
	// larger alignment is content motion, not drift. Votes start at the
	// anchor: the preamble alternation is periodic content that would
	// dilute the chart statistics.
	const scanSh = 8
	const acceptSh = 2.5
	for pass := 0; pass < passes; pass++ {
		vote := make([]float64, nLines-1)
		vok := make([]bool, nLines-1)
		for l := anchorLine; l+1 < nLines; l++ {
			s0, s1 := clk.start[l], clk.start[l+1]
			cs := make([]float64, 2*scanSh+1)
			best, bestI := -2.0, 0
			for sh := -scanSh; sh <= scanSh; sh++ {
				c := t.corrShifted(s0+8, s1+8+float64(sh), seg)
				cs[sh+scanSh] = c
				if c > best {
					best, bestI = c, sh
				}
			}
			// Honest content correlates strongly at alignment (phasing
			// ~1.0, chart structure 0.5+); a weak best (checkerboard
			// ~0.3, noise) means this pair has no alignment to offer.
			if best < 0.55 || math.Abs(float64(bestI)) > acceptSh {
				continue
			}
			// Correlation-weighted centre vote: line content is
			// piecewise flat, so the correlation top is a plateau, not
			// a point. Weighting each plateau position by its height
			// above the plateau floor centres the vote on the true
			// shift - an argmax ties to an arbitrary plateau edge, a
			// plain plateau mean ignores the tilt that noise and mixed
			// content give the top. Peaks outside +-2.5 hops are chart
			// features in motion (diagonals), rejected as clock votes.
			thr := best - 0.05
			var sw, swx float64
			for sh := -scanSh; sh <= scanSh; sh++ {
				if w := cs[sh+scanSh] - thr; w > 0 {
					sw += w
					swx += w * float64(sh)
				}
			}
			if sw <= 0 {
				continue
			}
			v := swx / sw
			if math.Abs(v) <= acceptSh {
				vote[l] = v
				vok[l] = true
			}
		}
		// Content-run veto: a diagonal coastline or weather front is
		// ONE feature crossing many consecutive lines at the same
		// per-line step, so it produces a long same-sign run of
		// substantial votes that no clock can own - a steady recorder
		// offset is absorbed by the anchor period (measured slant
		// residuals are ~0.03 hops, scattered) and recorder wander
		// reverses within its swing period, so legitimate clock votes
		// never form such runs. Measured on the fixture corpus
		// (2026-10-23): diagonal content runs 111-129 lines at
		// 0.66-1.92 hops/line; the veto preserves the steady-slant,
		// +-2500 ppm and wide-fast +-5000 ppm wander fixtures
		// (TestWEFAXSlantFixture, TestWEFAXWanderingClockDeskew,
		// TestWEFAXWideFastWanderDeskew).
		const vetoRun = 40
		const vetoMinMag = 0.5
		for l := anchorLine; l+1 < nLines; {
			if !vok[l] || math.Abs(vote[l]) < vetoMinMag {
				l++
				continue
			}
			pos := vote[l] > 0
			j := l
			for j+1 < nLines && vok[j] && (vote[j] > 0) == pos && math.Abs(vote[j]) >= vetoMinMag {
				j++
			}
			if j-l > vetoRun {
				for k := l; k < j; k++ {
					vok[k] = false
				}
			}
			l = j
		}
		// Median filter over +-15 lines; empty bins borrow the nearest
		// smoothed value so flat chart regions keep the local clock.
		sm := make([]float64, nLines-1)
		smOK := make([]bool, nLines-1)
		for l := 0; l+1 < nLines; l++ {
			var win []float64
			for k := l - 15; k <= l+15; k++ {
				if k >= 0 && k+1 < nLines && vok[k] {
					win = append(win, vote[k])
				}
			}
			if len(win) == 0 {
				continue
			}
			sort.Float64s(win)
			sm[l], smOK[l] = win[len(win)/2], true
		}
		last := 0.0
		seen := false
		for l := 0; l+1 < nLines; l++ {
			if smOK[l] {
				last, seen = sm[l], true
			} else if seen {
				sm[l], smOK[l] = last, true
			}
		}
		last = 0
		seen = false
		for l := nLines - 2; l >= 0; l-- {
			if smOK[l] {
				last, seen = sm[l], true
			} else if seen {
				sm[l], smOK[l] = last, true
			}
		}
		votes = 0
		for l := 0; l+1 < nLines; l++ {
			if smOK[l] {
				// Per-pass corrections are small: after the first pass
				// the grid is close, and no recorder changes speed by
				// more than ~0.6 hops (2400 ppm) per line within a
				// pass. Larger median corrections are content
				// artifacts, so clamp them.
				v := sm[l]
				if v > 0.6 {
					v = 0.6
				}
				if v < -0.6 {
					v = -0.6
				}
				clk.per[l] = (clk.start[l+1] - clk.start[l]) + v
				votes++
			} else {
				clk.per[l] = clk.start[l+1] - clk.start[l]
			}
			// The tracked period may follow real wander (the archive
			// demo swings +-3700 ppm) but never leaves +-1.2 hops of
			// the seeded period; beyond that the "correction" is chart
			// content, not clock.
			if d := clk.per[l] - anchorPer; d > 1.2 {
				clk.per[l] = anchorPer + 1.2
			} else if d < -1.2 {
				clk.per[l] = anchorPer - 1.2
			}
		}
		clk.per[nLines-1] = clk.per[nLines-2]
		clk.start[anchorLine] = anchorStart
		for l := anchorLine; l+1 < nLines; l++ {
			clk.start[l+1] = clk.start[l] + clk.per[l]
		}
		for l := anchorLine - 1; l >= 0; l-- {
			clk.start[l] = clk.start[l+1] - clk.per[l]
		}
	}
	// Drift statistics in ppm of the nominal line period, over the
	// chart region (from the anchor) only.
	first := true
	var sum float64
	var cnt float64
	for l := anchorLine; l < nLines; l++ {
		d := (clk.per[l]/float64(lineHops) - 1) * 1e6
		sum += d
		cnt++
		if first || d < minPPM {
			minPPM = d
		}
		if first || d > maxPPM {
			maxPPM = d
		}
		first = false
	}
	if cnt > 0 {
		medPPM = sum / cnt
	}
	return clk, votes, medPPM, minPPM, maxPPM
}

// renderCurve draws the image flying the fitted clock: line y starts at
// clk.start[fromLine+y] and spans clk.per hops. It also returns the raw
// brightness rows so the caller can score the deskew honestly.
func (t *wefaxTrace) renderCurve(clk *wefaxClock, fromLine, ppl int) (*image.Gray, int, []float64) {
	lastLine := len(clk.start) - 1
	for lastLine > fromLine && clk.start[lastLine] >= float64(len(t.ratio)-2) {
		lastLine--
	}
	if lastLine <= fromLine {
		return nil, 0, nil
	}
	nLines := lastLine - fromLine
	raw := make([]float64, nLines*ppl)
	for y := 0; y < nLines; y++ {
		l := fromLine + y
		start := clk.start[l]
		per := clk.per[l]
		step := per / float64(ppl)
		subs := int(step) + 1
		if subs < 2 {
			subs = 2
		}
		for x := 0; x < ppl; x++ {
			lo := start + float64(x)*step
			sum := 0.0
			for s2 := 0; s2 < subs; s2++ {
				sum += t.ratioAt(lo + step*float64(s2)/float64(subs))
			}
			raw[y*ppl+x] = sum / float64(subs)
		}
	}
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
	return img, nLines, raw
}

// deskewCorrelation is the honesty metric for the slant fix: the mean
// zero-mean correlation between adjacent rendered rows. A chart that is
// truly deskewed stacks its features into columns (correlation high);
// a slanted or structureless render does not.
func deskewCorrelation(raw []float64, ppl, lines int) float64 {
	if lines < 3 {
		return 0
	}
	var tot float64
	var pairs int
	for y := 0; y+1 < lines; y++ {
		a := raw[y*ppl : (y+1)*ppl]
		b := raw[(y+1)*ppl : (y+2)*ppl]
		var m1, m2 float64
		for x := 0; x < ppl; x++ {
			m1 += a[x]
			m2 += b[x]
		}
		m1 /= float64(ppl)
		m2 /= float64(ppl)
		var sab, saa, sbb float64
		for x := 0; x < ppl; x++ {
			u := a[x] - m1
			v := b[x] - m2
			sab += u * v
			saa += u * u
			sbb += v * v
		}
		if saa > 1e-9 && sbb > 1e-9 {
			tot += sab / math.Sqrt(saa*sbb)
			pairs++
		}
	}
	if pairs == 0 {
		return 0
	}
	return tot / float64(pairs)
}
