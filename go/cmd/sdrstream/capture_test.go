package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/decode"
	"github.com/chaim12345/sdrstream/go/internal/monitor"
	"github.com/chaim12345/sdrstream/go/internal/web"
)

func TestWriteCaptureArchiveWritesWAVAndProvenance(t *testing.T) {
	dir := t.TempDir()
	started := time.Date(2026, 10, 22, 9, 20, 0, 0, time.UTC)
	path, err := writeCaptureWAV(dir, decode.WriteWAV16([]int16{0, 1000, -1000}, 8000), captureMeta{
		Kind: "data", Mode: "ft8", Station: "http://kiwi.test:8073/",
		FrequencyHz: 7074000, Started: started, Detail: "CQ DL1ABC JO62",
		Target: "FT8 40m", Gate: "2 messages", Verified: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(filepath.Base(path), "DATA_7074kHz_ft8_kiwi_test_8073") {
		t.Fatalf("capture name = %q, want kind/frequency/mode/station", filepath.Base(path))
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() == 0 {
		t.Fatalf("capture missing or empty: info=%v err=%v", info, err)
	}
	sidecar, err := os.ReadFile(strings.TrimSuffix(path, ".wav") + ".txt")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"frequency_hz=7074000", "mode=ft8", "receiver=http://kiwi.test:8073/",
		"decode=CQ DL1ABC JO62", "kind=data", "target=FT8 40m",
		"gate=2 messages", "started=2026-10-22T09:20:00Z", "verified=true",
	} {
		if !strings.Contains(string(sidecar), want) {
			t.Errorf("sidecar missing %q:\n%s", want, sidecar)
		}
	}
}

func TestWriteCaptureArchiveUsesUniqueNamesForSameFrequency(t *testing.T) {
	dir := t.TempDir()
	meta := captureMeta{Kind: "voice", Mode: "speech", FrequencyHz: 5505000, Started: time.Now().UTC()}
	first, err := writeCaptureWAV(dir, decode.WriteWAV16([]int16{1, 2}, 8000), meta)
	if err != nil {
		t.Fatal(err)
	}
	second, err := writeCaptureWAV(dir, decode.WriteWAV16([]int16{3, 4}, 8000), meta)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("two captures used the same path %q", first)
	}
}

func TestPruneCapturesRemovesOldestAndSidecars(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	makeCapture := func(name string, size int, mod time.Time) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, strings.TrimSuffix(name, ".wav")+".txt"), []byte("kind=voice\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(filepath.Join(dir, name), mod, mod); err != nil {
			t.Fatal(err)
		}
	}
	makeCapture("old.wav", 100, now.Add(-2*time.Hour))
	makeCapture("middle.wav", 100, now.Add(-30*time.Minute))
	makeCapture("new.wav", 100, now.Add(-time.Minute))

	removed, _, err := pruneCaptures(dir, 0, time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want only the old capture", removed)
	}
	if _, err := os.Stat(filepath.Join(dir, "old.wav")); !os.IsNotExist(err) {
		t.Errorf("old.wav still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "old.txt")); !os.IsNotExist(err) {
		t.Errorf("old sidecar still exists: %v", err)
	}
	for _, name := range []string{"middle.wav", "new.wav"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s was pruned: %v", name, err)
		}
	}
}

func TestPruneCapturesHonorsSizeLimit(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	for i, name := range []string{"a.wav", "b.wav", "c.wav"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, make([]byte, 100), 0o644); err != nil {
			t.Fatal(err)
		}
		mod := now.Add(time.Duration(i-3) * time.Minute)
		if err := os.Chtimes(path, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
	removed, freed, err := pruneCaptures(dir, 150, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 || freed != 200 {
		t.Fatalf("removed=%d freed=%d, want 2/200", removed, freed)
	}
	if _, err := os.Stat(filepath.Join(dir, "c.wav")); err != nil {
		t.Errorf("newest capture was pruned: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.wav")); !os.IsNotExist(err) {
		t.Errorf("oldest capture survived: %v", err)
	}
}

func TestWriteActivitySidecarUpgradesVerification(t *testing.T) {
	dir := t.TempDir()
	server := web.New(dir, t.TempDir())
	wavPath := filepath.Join(dir, "DATA_5505kHz_voice_kiwi.test_1.wav")
	if err := os.WriteFile(wavPath, decode.WriteWAV16([]int16{0, 1}, 8000), 0o644); err != nil {
		t.Fatal(err)
	}
	activity := monitor.Activity{
		Started:     time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC),
		FrequencyHz: 5505000, Kind: "voice", Mode: "speech",
		Detail: "Shannon VOLMET wind 200 degrees", Target: "Shannon VOLMET 5505",
		Source: "kiwi.test:8073", AudioPath: wavPath, DurationSec: 25,
	}
	writeActivitySidecar(server, activity, true, "live gate passed")
	sidecar, err := os.ReadFile(strings.TrimSuffix(wavPath, ".wav") + ".txt")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"kind=voice", "target=Shannon VOLMET 5505", "verified=true", "gate=live gate passed", "duration_sec=25.000"} {
		if !strings.Contains(string(sidecar), want) {
			t.Errorf("sidecar missing %q:\n%s", want, sidecar)
		}
	}
}

func TestWavDurationSec(t *testing.T) {
	wav := decode.WriteWAV16(make([]int16, 8000), 8000)
	if got := wavDurationSec(wav); got != 1 {
		t.Fatalf("duration = %v, want 1s", got)
	}
	if got := wavDurationSec(nil); got != 0 {
		t.Fatalf("empty duration = %v, want 0", got)
	}
}
