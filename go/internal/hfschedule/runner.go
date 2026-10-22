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
	Attempts         map[string]Audit
	ReceiverFailures map[string]time.Time
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
		var receiver Receiver
		for _, candidate := range receivers {
			if failed, ok := state.ReceiverFailures[candidate.URL]; !ok || now.Sub(failed) >= time.Hour {
				receiver = candidate
				break
			}
		}
		if receiver.URL == "" {
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
		if err := saveJSON(filepath.Join(r.Dir, o.Key()+".json"), a); err != nil {
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
