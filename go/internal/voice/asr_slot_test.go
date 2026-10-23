package voice

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// writeFakeWhisper installs a shell script that stands in for whisper-cli:
// it logs start/end events (nanoseconds) to logPath, honours -of/-m, and
// writes whisper -ojf JSON. confFor maps a model substring to the emitted
// confidence; empty map always emits 0.9. exitCode, when non-zero, makes
// the script fail like a crashed binary.
func writeFakeWhisper(t *testing.T, dir, logPath string, confFor map[string]float64, exitCode int) string {
	t.Helper()
	var cases strings.Builder
	for modelPart, conf := range confFor {
		fmt.Fprintf(&cases, "case \"$model\" in *%s*) conf=%s;; esac\n", modelPart, strconv.FormatFloat(conf, 'f', -1, 64))
	}
	script := fmt.Sprintf(`#!/bin/sh
model=
out=
while [ $# -gt 0 ]; do
  case "$1" in
    -m) model=$2; shift 2 ;;
    -of) out=$2; shift 2 ;;
    *) shift ;;
  esac
done
conf=0.9
%s
if [ %d -ne 0 ]; then
  echo "fake whisper crash" >&2
  exit %d
fi
echo "s $(date +%%s%%N)" >> %s
sleep 0.15
echo "e $(date +%%s%%N)" >> %s
printf '{"result":{"language":"en"},"transcription":[{"text":" clear forecast","tokens":[{"text":"clear","p":'"$conf"'},{"text":"forecast","p":'"$conf"'}]}]}' > "$out.json"
`, cases.String(), exitCode, exitCode, logPath, logPath)
	path := filepath.Join(dir, "fake-whisper")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// maxOverlap computes the largest number of simultaneously open start/end
// intervals in the fake whisper event log.
func maxOverlap(t *testing.T, logPath string) int {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	type event struct {
		ns    int64
		delta int
	}
	var events []event
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var kind string
		var ns int64
		if _, err := fmt.Sscanf(line, "%s %d", &kind, &ns); err != nil {
			t.Fatalf("bad log line %q: %v", line, err)
		}
		delta := 1
		if kind == "e" {
			delta = -1
		}
		events = append(events, event{ns, delta})
	}
	for i := 1; i < len(events); i++ {
		for j := i; j > 0 && events[j].ns < events[j-1].ns; j-- {
			events[j], events[j-1] = events[j-1], events[j]
		}
	}
	cur, max := 0, 0
	for _, e := range events {
		cur += e.delta
		if cur > max {
			max = cur
		}
	}
	return max
}

// The quality pipeline used to bypass the single local whisper slot: N
// concurrent chunked ASR runs forked N multi-GB whisper processes while
// Transcribe callers queued on one. runWhisper now takes the slot, so
// concurrent chunk calls serialise exactly like plain Transcribe calls.
func TestRunWhisperSerialisesConcurrentCallers(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.log")
	binary := writeFakeWhisper(t, dir, logPath, nil, 0)
	const callers = 4
	var wg sync.WaitGroup
	errs := make([]error, callers)
	texts := make([]string, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			text, _, _, err := runWhisper(context.Background(), binary, "model", filepath.Join(dir, fmt.Sprintf("c%d.wav", i)), Options{})
			errs[i] = err
			texts[i] = text
		}(i)
	}
	wg.Wait()
	for i := range errs {
		if errs[i] != nil {
			t.Fatalf("caller %d: %v", i, errs[i])
		}
		if texts[i] != "clear forecast" {
			t.Errorf("caller %d text = %q", i, texts[i])
		}
	}
	if max := maxOverlap(t, logPath); max > 1 {
		t.Errorf("concurrent whisper processes peaked at %d, slot caps at 1", max)
	}
}

// A caller cancelled while QUEUING for the slot must fail with the ctx
// error and must not leak or consume a slot.
func TestRunWhisperCancelWhileWaitingForSlot(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.log")
	binary := writeFakeWhisper(t, dir, logPath, nil, 0)
	// Occupy the single slot so the next caller has to queue.
	transcribeSlot <- struct{}{}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, _, _, err := runWhisper(ctx, binary, "model", filepath.Join(dir, "q.wav"), Options{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("queued cancel err = %v, want deadline exceeded", err)
	}
	if len(transcribeSlot) != 1 {
		t.Errorf("queued caller disturbed the slot: len = %d, want 1", len(transcribeSlot))
	}
	<-transcribeSlot // free our own occupancy
	// The slot is healthy afterwards: a normal call runs to completion.
	text, _, _, err := runWhisper(context.Background(), binary, "model", filepath.Join(dir, "ok.wav"), Options{})
	if err != nil || text != "clear forecast" {
		t.Errorf("post-cancel call: text %q err %v", text, err)
	}
}

// A crashed whisper run must release the slot for the next caller.
func TestRunWhisperErrorReleasesSlot(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.log")
	crasher := writeFakeWhisper(t, dir, logPath, nil, 1)
	if _, _, _, err := runWhisper(context.Background(), crasher, "model", filepath.Join(dir, "x.wav"), Options{}); err == nil {
		t.Fatal("crashed binary reported success")
	}
	// With the slot leaked, this would queue forever; a short ctx turns a
	// leak into a failure instead of a hang.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	good := writeFakeWhisper(t, dir, logPath, nil, 0)
	text, _, _, err := runWhisper(ctx, good, "model", filepath.Join(dir, "y.wav"), Options{})
	if err != nil || text != "clear forecast" {
		t.Errorf("call after error: text %q err %v (slot leaked?)", text, err)
	}
}

// The low-confidence fallback to the second model must survive the slot
// wiring: primary returns junk confidence, the fallback model answers
// confidently, and the result carries the fallback text and flag.
func TestTranscribeASRFallbackUnderSlot(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.log")
	binary := writeFakeWhisper(t, dir, logPath, map[string]float64{"primary": 0.1, "fallback": 0.95}, 0)
	primary := filepath.Join(dir, "primary.bin")
	fallback := filepath.Join(dir, "fallback.bin")
	for _, m := range []string{primary, fallback} {
		if err := os.WriteFile(m, []byte("fake model"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rng := rand.New(rand.NewSource(3))
	rate := 8000
	var samples []float64
	samples = append(samples, noise(rate, 1, 0.002, rng)...)
	samples = append(samples, tone(rate, 1.5, 0.3)...)
	samples = append(samples, noise(rate, 1, 0.002, rng)...)
	res, err := TranscribeASR(context.Background(), samples, rate, ASROptions{
		Options:       Options{Binary: binary, Model: primary},
		FallbackModel: fallback,
		MinConfidence: 0.5,
		ChunkSeconds:  30,
	})
	if err != nil {
		t.Fatalf("TranscribeASR: %v", err)
	}
	if res.Text != "clear forecast" {
		t.Errorf("text = %q, want fallback transcript", res.Text)
	}
	if !res.Fallback {
		t.Error("Fallback flag not set after low-confidence primary")
	}
	if res.Model != "fallback.bin" {
		t.Errorf("model = %q, want fallback.bin", res.Model)
	}
}
