package decode

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"math"
	"sort"
)

// NOAA APT (Automatic Picture Transmission), 137 MHz weather satellites.
//
// The satellite FM-modulates a 2400 Hz subcarrier that is amplitude
// modulated by the picture. After FM demodulation to audio, the picture is
// the envelope of the 2400 Hz tone, sampled at 4160 words per second, 2080
// words per line (two lines per second). Every line starts with Sync A
// (seven cycles of a 1040 Hz square wave) and has Sync B (seven cycles of
// 832 Hz) at the start of the second channel, word 1040. Those two fixed
// patterns are what separate a picture from noise, and they are what the
// verification gate below measures.
const (
	aptWordRate = 4160
	aptLineLen  = 2080
)

// APTResult is a decoded APT recording.
type APTResult struct {
	Image *image.Gray // aptLineLen wide, one row per half second
	// Lines is the number of rows covered, LinesLocked the rows whose
	// Sync A was found where the line cadence says it should be.
	Lines       int
	LinesLocked int
	// SyncB is the fraction of locked lines whose Sync B (independent
	// pattern at word 1040) also correlates.
	SyncB float64
	// MeanSyncA is the mean normalised correlation of locked lines.
	MeanSyncA float64
	StartSec  float64
}

// Verified reports whether the recording passes the strict gate: at least
// 60 locked lines (30 s) spanning most of the covered rows, strong Sync A,
// and Sync B present on most of them. A noise recording or a speech clip
// cannot hold two independent patterns at a 0.5 s cadence.
func (r *APTResult) Verified() (bool, string) {
	if r == nil || r.LinesLocked < 60 {
		return false, "fewer than 60 sync-locked lines"
	}
	if float64(r.LinesLocked) < 0.6*float64(r.Lines) {
		return false, "sync lock covers under 60% of rows"
	}
	if r.MeanSyncA < 0.55 {
		return false, "Sync A correlation weak"
	}
	if r.SyncB < 0.6 {
		return false, "Sync B missing"
	}
	return true, "APT Sync A/B lock"
}

// PNG encodes the picture.
func (r *APTResult) PNG() ([]byte, error) {
	var b bytes.Buffer
	if err := png.Encode(&b, r.Image); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// aptEnvelope demodulates the 2400 Hz AM subcarrier and returns the
// envelope sampled at aptWordRate.
func aptEnvelope(samples []float64, rate int) []float64 {
	env, _ := aptEnvelopeContext(context.Background(), samples, rate)
	return env
}

// aptEnvelopeContext is aptEnvelope with cancellation checks inside its
// per-sample loops.
func aptEnvelopeContext(ctx context.Context, samples []float64, rate int) ([]float64, error) {
	n := len(samples)
	if n == 0 || rate < 6000 {
		return nil, nil
	}
	// Quadrature mix with 2400 Hz, boxcar low-pass over ~2 carrier cycles
	// (cumulative sums with fractional edges are unnecessary here: the
	// window only has to null the 4800 Hz image).
	w := int(math.Round(float64(rate) / 1200))
	cI := make([]float64, n+1)
	cQ := make([]float64, n+1)
	step := 2 * math.Pi * 2400 / float64(rate)
	for i, v := range samples {
		if i&(1<<15) == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		ph := step * float64(i)
		cI[i+1] = cI[i] + v*math.Cos(ph)
		cQ[i+1] = cQ[i] + v*math.Sin(ph)
	}
	mag := make([]float64, n)
	for i := 0; i < n; i++ {
		if i&(1<<15) == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		a, b := i-w/2, i+w/2
		if a < 0 {
			a = 0
		}
		if b > n {
			b = n
		}
		if b-a < 2 {
			continue
		}
		di, dq := cI[b]-cI[a], cQ[b]-cQ[a]
		mag[i] = 2 * math.Hypot(di, dq) / float64(b-a)
	}
	out := int(float64(n) * aptWordRate / float64(rate))
	env := make([]float64, out)
	ratio := float64(rate) / aptWordRate
	for i := range env {
		pos := float64(i) * ratio
		i0 := int(pos)
		if i0 >= n-1 {
			env[i] = mag[n-1]
			continue
		}
		f := pos - float64(i0)
		env[i] = mag[i0]*(1-f) + mag[i0+1]*f
	}
	return env, nil
}

// aptTemplate builds the zero-mean sync template: lead-in of 4 dark
// words, then cycles of the given period (in words), half low and half
// high, as in the NOAA APT format description.
func aptTemplate(period, cycles, lead int) []float64 {
	t := make([]float64, 0, lead+period*cycles)
	for i := 0; i < lead; i++ {
		t = append(t, 0)
	}
	for c := 0; c < cycles; c++ {
		for j := 0; j < period; j++ {
			if j >= period/2 {
				t = append(t, 1)
			} else {
				t = append(t, 0)
			}
		}
	}
	return t
}

// nccAt is the normalised correlation of tpl against env at off.
func nccAt(env, tpl []float64, tm, tn float64, off int) float64 {
	if off < 0 || off+len(tpl) > len(env) {
		return -1
	}
	seg := env[off : off+len(tpl)]
	var s, ss, st float64
	for i, v := range seg {
		s += v
		ss += v * v
		st += v * tpl[i]
	}
	n := float64(len(tpl))
	mean := s / n
	vx := ss - n*mean*mean
	if vx <= 1e-12 {
		return -1
	}
	cov := st - n*mean*tm
	return cov / math.Sqrt(vx*tn)
}

// APTDecode decodes NOAA APT audio (FM-demodulated, any rate >= 8 kHz).
func APTDecode(samples []float64, rate int) *APTResult {
	r, _ := APTDecodeContext(context.Background(), samples, rate)
	return r
}

// APTDecodeContext is APTDecode with cancellation: a cancelled context
// returns (nil, ctx.Err()); ordinary "no APT signal" stays (nil, nil).
func APTDecodeContext(ctx context.Context, samples []float64, rate int) (*APTResult, error) {
	env, err := aptEnvelopeContext(ctx, samples, rate)
	if err != nil {
		return nil, err
	}
	if len(env) < 4*aptLineLen {
		return nil, nil
	}
	tplA := aptTemplate(4, 7, 4)
	var tm, tn float64
	for _, v := range tplA {
		tm += v
	}
	tm /= float64(len(tplA))
	for _, v := range tplA {
		tn += (v - tm) * (v - tm)
	}
	// Normalised correlation at every offset; candidates are local maxima
	// above 0.45.
	type cand struct {
		pos   int
		score float64
	}
	var cands []cand
	last := len(env) - len(tplA)
	sc := make([]float64, last+1)
	for i := 0; i <= last; i++ {
		if i&4095 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		sc[i] = nccAt(env, tplA, tm, tn, i)
	}
	for i := 3; i < last-3; i++ {
		s := sc[i]
		if s < 0.45 {
			continue
		}
		if s >= sc[i-1] && s >= sc[i+1] && s >= sc[i-2] && s >= sc[i+2] && s >= sc[i-3] && s >= sc[i+3] {
			cands = append(cands, cand{i, s})
		}
	}
	// Greedy by score with a one-line exclusion, then order by position.
	sort.Slice(cands, func(a, b int) bool { return cands[a].score > cands[b].score })
	var picked []cand
	for _, c := range cands {
		ok := true
		for _, p := range picked {
			if d := c.pos - p.pos; d > -1500 && d < 1500 {
				ok = false
				break
			}
		}
		if ok {
			picked = append(picked, c)
		}
	}
	if len(picked) < 4 {
		return nil, nil
	}
	sort.Slice(picked, func(a, b int) bool { return picked[a].pos < picked[b].pos })
	// Keep only peaks consistent with the 2080-word cadence: a peak is
	// locked when a neighbour (previous or next kept peak) sits an integer
	// number of lines away within +-6 words per line of elapsed time.
	locked := make([]bool, len(picked))
	for i := range picked {
		for _, j := range []int{i - 1, i + 1} {
			if j < 0 || j >= len(picked) {
				continue
			}
			d := picked[j].pos - picked[i].pos
			if d < 0 {
				d = -d
			}
			k := int(math.Round(float64(d) / aptLineLen))
			if k >= 1 && k <= 40 && math.Abs(float64(d-k*aptLineLen)) <= float64(6*k) {
				locked[i] = true
			}
		}
	}
	var lp []cand
	for i, c := range picked {
		if locked[i] {
			lp = append(lp, c)
		}
	}
	if len(lp) < 4 {
		return nil, nil
	}
	first, lastP := lp[0].pos, lp[len(lp)-1].pos
	rows := (lastP-first)/aptLineLen + 1
	img := image.NewGray(image.Rect(0, 0, aptLineLen, rows))
	// Robust brightness scaling from the 2% / 98% percentiles.
	step := 7
	var vals []float64
	for i := first; i < lastP+aptLineLen && i < len(env); i += step {
		vals = append(vals, env[i])
	}
	sort.Float64s(vals)
	lo, hi := vals[len(vals)/50], vals[len(vals)*49/50]
	if hi-lo < 1e-9 {
		return nil, nil
	}
	put := func(row int, start int) {
		if row < 0 || row >= rows {
			return
		}
		for x := 0; x < aptLineLen; x++ {
			idx := start + x
			if idx < 0 || idx >= len(env) {
				continue
			}
			v := (env[idx] - lo) / (hi - lo) * 255
			img.SetGray(x, row, color.Gray{uint8(math.Max(0, math.Min(255, v)))})
		}
	}
	tplB := aptTemplate(5, 7, 4)
	var bm, bn float64
	for _, v := range tplB {
		bm += v
	}
	bm /= float64(len(tplB))
	for _, v := range tplB {
		bn += (v - bm) * (v - bm)
	}
	res := &APTResult{Image: img, Lines: rows, StartSec: float64(first) / aptWordRate}
	var sumA float64
	bOK := 0
	for _, c := range lp {
		row := (c.pos - first + aptLineLen/2) / aptLineLen
		put(row, c.pos)
		res.LinesLocked++
		sumA += c.score
		// Sync B sits at word 1040 of the line, lead-in included.
		best := -1.0
		for d := -3; d <= 3; d++ {
			if s := nccAt(env, tplB, bm, bn, c.pos+1040+d); s > best {
				best = s
			}
		}
		if best >= 0.4 {
			bOK++
		}
	}
	res.MeanSyncA = sumA / float64(res.LinesLocked)
	res.SyncB = float64(bOK) / float64(res.LinesLocked)
	return res, nil
}
