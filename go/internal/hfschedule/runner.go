package hfschedule

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/decode"
	"github.com/chaim12345/sdrstream/go/internal/websdr"
)

// Receiver is an operator-selected public receiver. Ranges are explicit in kHz;
// no speculative wideband tuning or directory scraping is performed.
type Receiver struct {
	URL            string
	Type           string // kiwi or websdr
	MinKHz, MaxKHz float64
	Priority       int // lower first; order equal-priority receivers by URL
}

func ReceiversFor(rs []Receiver, t decode.HFTarget) []Receiver {
	var out []Receiver
	for _, r := range rs {
		if r.URL != "" && (r.Type == "kiwi" || r.Type == "websdr") && r.MinKHz >= 0 && r.MaxKHz > r.MinKHz && t.DialKHz >= r.MinKHz && t.CarrierKHz <= r.MaxKHz {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Priority == out[j].Priority {
			return out[i].URL < out[j].URL
		}
		return out[i].Priority < out[j].Priority
	})
	return out
}

type RecordFunc func(context.Context, Receiver, decode.HFTarget, time.Duration) (decode.HFResult, error)

func Record(ctx context.Context, r Receiver, t decode.HFTarget, d time.Duration) (decode.HFResult, error) {
	if r.Type == "kiwi" {
		return decode.RunHFTarget(ctx, r.URL, t, d)
	}
	opts := websdr.DefaultOptions()
	opts.Reconnect = false
	opts.OpenTimeout = 10 * time.Second
	opts.IdleTimeout = 15 * time.Second
	c, err := websdr.New(r.URL, opts)
	if err != nil {
		return decode.HFResult{}, err
	}
	defer c.Close()
	if err = c.Connect(ctx); err != nil {
		return decode.HFResult{}, err
	}
	if err = c.Tune(ctx, t.DialKHz, t.Mode, nil); err != nil {
		return decode.HFResult{}, err
	}
	started := time.Now().UTC()
	s, rate, err := c.Record(ctx, d)
	if err != nil {
		return decode.HFResult{}, err
	}
	result := decode.HFDecodeForScan(ctx, t, r.URL, s, rate)
	result.Started = started
	result.AudioWAV = decode.WriteWAV16(s, rate)
	return result, nil
}

// ReceiverMetrics is the persisted per-receiver success record. A success
// is a complete, valid capture (status recorded or silent); refusals,
// timeouts and truncated audio are failures.
type ReceiverMetrics struct {
	Attempts            int       `json:"attempts"`
	Successes           int       `json:"successes"`
	Failures            int       `json:"failures"`
	ConsecutiveFailures int       `json:"consecutive_failures,omitempty"`
	LastSuccess         time.Time `json:"last_success,omitempty"`
	LastFailure         time.Time `json:"last_failure,omitempty"`
	LastError           string    `json:"last_error,omitempty"`
}

// SuccessRate ranks receivers; no history rates neutral so a new receiver
// keeps its operator priority until it has a record.
func (m ReceiverMetrics) SuccessRate() float64 {
	if m.Attempts == 0 {
		return 0.5
	}
	return float64(m.Successes) / float64(m.Attempts)
}

// cooldown grows with the consecutive-failure streak: one hour after the
// first failure, doubling per further failure, capped at a day.
func (m ReceiverMetrics) cooldown() time.Duration {
	if m.ConsecutiveFailures <= 0 {
		return 0
	}
	d := time.Hour
	for i := 1; i < m.ConsecutiveFailures && d < 24*time.Hour; i++ {
		d *= 2
	}
	return min(d, 24*time.Hour)
}

// benched reports whether the receiver is inside its failure cooldown.
func (m ReceiverMetrics) benched(now time.Time) bool {
	if m.ConsecutiveFailures <= 0 || m.LastFailure.IsZero() {
		return false
	}
	return now.Sub(m.LastFailure) < m.cooldown()
}

// noteResult folds one capture outcome into the metrics.
func (m *ReceiverMetrics) noteResult(ok bool, errText string, now time.Time) {
	m.Attempts++
	if ok {
		m.Successes++
		m.ConsecutiveFailures = 0
		m.LastSuccess = now
		m.LastError = ""
		return
	}
	m.Failures++
	m.ConsecutiveFailures++
	m.LastFailure = now
	m.LastError = errText
}

// selectReceiver picks the healthiest eligible receiver: benched receivers
// sit out their cooldown; among the rest the highest persisted success rate
// wins, ties keep the operator priority order ReceiversFor established.
func selectReceiver(receivers []Receiver, metrics map[string]ReceiverMetrics, now time.Time) (Receiver, bool) {
	best := -1
	bestRate := -1.0
	bestStreak := 1 << 30
	for i, r := range receivers {
		m := metrics[r.URL]
		if m.benched(now) {
			continue
		}
		rate := m.SuccessRate()
		if best < 0 || rate > bestRate || (rate == bestRate && m.ConsecutiveFailures < bestStreak) {
			best, bestRate, bestStreak = i, rate, m.ConsecutiveFailures
		}
	}
	if best < 0 {
		return Receiver{}, false
	}
	return receivers[best], true
}

type Audit struct {
	Occurrence          Occurrence
	Receiver            Receiver
	Started, Finished   time.Time
	Status              string // recorded, silent, failed or interrupted; never implies decode validity
	Error               string `json:",omitempty"`
	WAV, PNG, Text      string `json:",omitempty"`
	SampleRate, Samples int
	Peak                int
	RMS                 float64
	DecodeLive          bool
	DecodeReason        string
}
type State struct {
	Attempts map[string]Audit
	// ReceiverFailures is kept for backward compatibility with existing
	// state files; Metrics supersedes it for selection.
	ReceiverFailures map[string]time.Time
	// Metrics holds the persisted per-receiver success record, keyed by
	// receiver URL.
	Metrics map[string]ReceiverMetrics `json:",omitempty"`
}
type Runner struct {
	Dir       string
	Receivers []Receiver
	Listen    time.Duration
	Record    RecordFunc
	Now       func() time.Time
	// At most one capture is performed per RunOnce call, one connection at a time.
}

func atomicWrite(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".hf-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
func saveJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(path, append(b, '\n'))
}
func (r *Runner) RunOnce(ctx context.Context, entries []Entry) (*Audit, error) {
	now := time.Now().UTC()
	if r.Now != nil {
		now = r.Now().UTC()
	}
	listen := r.Listen
	if listen <= 0 {
		listen = 30 * time.Second
	}
	if listen > 20*time.Minute {
		return nil, fmt.Errorf("capture exceeds 20 minute polite limit")
	}
	if err := os.MkdirAll(r.Dir, 0700); err != nil {
		return nil, err
	}
	lock := filepath.Join(r.Dir, "scheduler.lock")
	if err := os.Mkdir(lock, 0700); err != nil {
		return nil, fmt.Errorf("scheduler already running or stale lock %s: %w", lock, err)
	}
	defer os.Remove(lock)
	// A crash leaves a lock deliberately. The operator must check for a running
	// process before removing it. Never silently run two schedulers on one store.
	state := State{Attempts: map[string]Audit{}}
	statePath := filepath.Join(r.Dir, "state.json")
	if b, err := os.ReadFile(statePath); err == nil {
		if err = json.Unmarshal(b, &state); err != nil {
			return nil, fmt.Errorf("corrupt state: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if state.ReceiverFailures == nil {
		state.ReceiverFailures = map[string]time.Time{}
	}
	if state.Metrics == nil {
		state.Metrics = map[string]ReceiverMetrics{}
	}
	if state.Attempts == nil {
		state.Attempts = map[string]Audit{}
	}
	for k, a := range state.Attempts {
		if a.Occurrence.End.Before(now.AddDate(0, 0, -7)) {
			delete(state.Attempts, k)
		}
	}
	for _, o := range Plan(entries, now, 0) {
		if prev, ok := state.Attempts[o.Key()]; ok {
			if prev.Status == "recorded" || now.Sub(prev.Finished) < time.Minute || prev.Status == "interrupted" {
				continue
			}
		}
		remaining := o.End.Sub(now)
		duration := min(listen, remaining)
		if duration < time.Second {
			continue
		}
		receivers := ReceiversFor(r.Receivers, o.Entry.Target)
		if len(receivers) == 0 {
			continue
		}
		receiver, ok := selectReceiver(receivers, state.Metrics, now)
		if !ok {
			continue
		}
		// Persist before touching the receiver: restarts cannot duplicate in-flight work.
		a := Audit{Occurrence: o, Receiver: receiver, Started: now, Finished: now, Status: "interrupted"}
		state.Attempts[o.Key()] = a
		if err := saveJSON(statePath, state); err != nil {
			return nil, err
		}
		record := r.Record
		if record == nil {
			record = Record
		}
		deadline := time.Now().Add(duration + 25*time.Second)
		if o.End.Before(deadline) {
			deadline = o.End
		}
		captureCtx, cancel := context.WithDeadline(ctx, deadline)
		result, err := record(captureCtx, receiver, o.Entry.Target, duration)
		cancel()
		a.Finished = time.Now().UTC()
		if r.Now != nil {
			a.Finished = r.Now().UTC()
		}
		a.Status = "failed"
		if err != nil {
			a.Error = err.Error()
			state.ReceiverFailures[receiver.URL] = now
		} else if len(result.AudioWAV) < 46 || string(result.AudioWAV[:4]) != "RIFF" {
			a.Error = "capture returned no PCM WAV"
			state.ReceiverFailures[receiver.URL] = now
		} else {
			wav := result.AudioWAV
			a.SampleRate = int(binary.LittleEndian.Uint32(wav[24:28]))
			a.Samples = (len(wav) - 44) / 2
			for i := 44; i+1 < len(wav); i += 2 {
				v := int(int16(binary.LittleEndian.Uint16(wav[i : i+2])))
				a.Peak = max(a.Peak, abs(v))
				a.RMS += float64(v) * float64(v)
			}
			a.RMS = math.Sqrt(a.RMS / float64(a.Samples))
			a.Status = "recorded"
			if a.Peak == 0 {
				a.Status = "silent"
			}
			if a.SampleRate <= 0 || float64(a.Samples) < 0.9*duration.Seconds()*float64(a.SampleRate) {
				a.Status = "failed"
				a.Error = "short capture or invalid sample rate"
				state.ReceiverFailures[receiver.URL] = now
			}
			base := filepath.Join(r.Dir, o.Key())
			a.WAV = base + ".wav"
			if err = atomicWrite(a.WAV, wav); err != nil {
				return nil, err
			}
			a.DecodeLive = result.Live() && a.Status == "recorded"
			a.DecodeReason = result.Gate.Reason
			if len(result.ImagePNG) > 0 {
				a.PNG = base + ".png"
				if err = atomicWrite(a.PNG, result.ImagePNG); err != nil {
					return nil, err
				}
			}
			if result.Text != "" {
				a.Text = base + ".txt"
				if err = atomicWrite(a.Text, []byte(result.Text)); err != nil {
					return nil, err
				}
			}
		}
		m := state.Metrics[receiver.URL]
		m.noteResult(a.Status == "recorded" || a.Status == "silent", a.Error, now)
		state.Metrics[receiver.URL] = m
		if err := saveJSON(filepath.Join(r.Dir, o.Key()+".json"), a); err != nil {
			return nil, err
		}
		if err := AppendLedger(r.Dir, a); err != nil {
			return nil, err
		}
		state.Attempts[o.Key()] = a
		if err := saveJSON(statePath, state); err != nil {
			return nil, err
		}
		return &a, nil
	}
	return nil, nil
}
func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
