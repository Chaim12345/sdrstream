package capture

import (
	"github.com/chaim12345/sdrstream/go/internal/decode"
	"math"
	"time"
)

type HFWindow struct {
	Mode        string
	FrequencyHz float64
	Start, End  time.Time
	Samples     []float64
	Rate        int
}

func WindowStart(at time.Time, mode string) time.Time {
	period := 15 * time.Second
	if mode == "wspr" {
		period = 2 * time.Minute
	}
	n := at.UTC().UnixNano()
	p := int64(period)
	return time.Unix(0, n-n%p).UTC()
}
func NewHFWindow(at time.Time, mode string, freq float64, rate int) *HFWindow {
	start := WindowStart(at, mode)
	dur := 15 * time.Second
	if mode == "wspr" {
		dur = 2 * time.Minute
	}
	return &HFWindow{Mode: mode, FrequencyHz: freq, Start: start, End: start.Add(dur), Rate: rate}
}

// Append maps a time-stamped sample block into the full aligned window,
// inserting zero fill for gaps, and ignoring samples outside its boundaries.
func (w *HFWindow) Append(at time.Time, samples []float64) {
	if at.IsZero() || len(samples) == 0 || w.Rate <= 0 {
		return
	}
	total := int(w.End.Sub(w.Start).Seconds() * float64(w.Rate))
	if len(w.Samples) >= total {
		return
	}
	if w.Start.IsZero() {
		return
	}
	current := w.Start.Add(time.Duration(len(w.Samples)) * time.Second / time.Duration(w.Rate))
	if at.After(current) {
		gap := int(math.Round(at.Sub(current).Seconds() * float64(w.Rate)))
		if gap > 0 {
			w.Samples = append(w.Samples, make([]float64, min(gap, total-len(w.Samples)))...)
		}
	}
	offset := int(math.Round(w.Start.Sub(at).Seconds() * float64(w.Rate)))
	from := max(0, offset)
	to := min(len(samples), total-offset)
	if to <= from {
		return
	}
	if offset < 0 {
		skip := -offset
		if skip >= len(samples) {
			return
		}
		from = skip
		to = len(samples)
	}
	copyAt := int(math.Round(at.Sub(w.Start).Seconds()*float64(w.Rate))) + from
	if copyAt < 0 {
		return
	}
	if copyAt > len(w.Samples) {
		w.Samples = append(w.Samples, make([]float64, copyAt-len(w.Samples))...)
	}
	if copyAt < len(w.Samples) {
		from += len(w.Samples) - copyAt
		copyAt = len(w.Samples)
	}
	n := min(to-from, total-copyAt)
	if n > 0 {
		w.Samples = append(w.Samples, samples[from:from+n]...)
	}
}
func (w *HFWindow) Complete() bool {
	return w.Rate > 0 && len(w.Samples) >= int(w.End.Sub(w.Start).Seconds()*float64(w.Rate))
}
func (w *HFWindow) Decode() []string {
	if !w.Complete() {
		return nil
	}
	switch w.Mode {
	case "ft8":
		m := decode.FTXDecodeStream(w.Samples, w.Rate, float64(w.Start.Unix()), "FT8")
		o := make([]string, 0, len(m))
		for _, x := range m {
			o = append(o, x.Text)
		}
		return o
	case "wspr":
		o := []string{}
		for _, win := range decode.WSPRWindows(w.Samples, w.Rate, float64(w.Start.Unix())) {
			for _, m := range win.Messages {
				o = append(o, m.Text)
			}
		}
		return o
	}
	return nil
}
