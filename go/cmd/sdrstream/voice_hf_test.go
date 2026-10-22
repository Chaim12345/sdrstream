package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/decode"
	"github.com/chaim12345/sdrstream/go/internal/voice"
	"github.com/chaim12345/sdrstream/go/internal/web"
)

// TestKiwiVoiceActivityUsesNIMTranscript pins the bridge from a captured
// Kiwi HF voice target to the gallery: decodeHF leaves voice unverified, so
// the scanner must transcribe the WAV and apply the live gate itself.
func TestKiwiVoiceActivityUsesNIMTranscript(t *testing.T) {
	old := transcribeHFVoice
	transcribeHFVoice = func(context.Context, []float64, int) (voice.Transcript, error) {
		return voice.Transcript{Text: "Shannon VOLMET, wind 200 degrees, 7 knots."}, nil
	}
	defer func() { transcribeHFVoice = old }()

	audio, err := os.ReadFile("../../../tests/data/real_volmet_shannon_5505_usb.wav")
	if err != nil {
		t.Fatal(err)
	}
	result := decode.HFResult{
		Target:   decode.HFTarget{Name: "Shannon VOLMET 5505", Kind: "voice", DialKHz: 5505.0, Mode: "usb"},
		Server:   "kiwi.test",
		Started:  time.Now().UTC(),
		AudioWAV: audio,
	}
	activity, err := kiwiVoiceActivity(context.Background(), "kiwi.test", result, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if activity.Kind != "voice" || activity.Mode != "speech" {
		t.Errorf("activity = %s/%s, want voice/speech", activity.Kind, activity.Mode)
	}
	if !strings.Contains(activity.Detail, "200 degrees") {
		t.Errorf("detail = %q, want the transcript", activity.Detail)
	}
	if activity.FrequencyHz != 5505000 {
		t.Errorf("frequency = %.0f Hz, want 5505000", activity.FrequencyHz)
	}
	if activity.AudioPath == "" {
		t.Fatal("verified voice activity has no audio capture")
	}
	if info, err := os.Stat(activity.AudioPath); err != nil || info.Size() != int64(len(audio)) {
		t.Errorf("capture %q: info=%v err=%v, want %d bytes", activity.AudioPath, info, err, len(audio))
	}
}

func TestKiwiVoiceActivityRejectsEmptyTranscript(t *testing.T) {
	old := transcribeHFVoice
	transcribeHFVoice = func(context.Context, []float64, int) (voice.Transcript, error) {
		return voice.Transcript{}, nil
	}
	defer func() { transcribeHFVoice = old }()

	audio, err := os.ReadFile("../../../tests/data/real_volmet_shannon_5505_usb.wav")
	if err != nil {
		t.Fatal(err)
	}
	result := decode.HFResult{
		Target:   decode.HFTarget{Name: "Shannon VOLMET 5505", Kind: "voice", DialKHz: 5505.0, Mode: "usb"},
		Started:  time.Now().UTC(),
		AudioWAV: audio,
	}
	if _, err := kiwiVoiceActivity(context.Background(), "kiwi.test", result, t.TempDir()); err == nil {
		t.Fatal("empty transcript passed the live gate")
	}
}

// TestEnsureScanKeepsDecodedResult pins the scan-board fix: a repeat Kiwi
// visit used to call addScan("queued") for every target and erase the
// transcript from the previous visit before the receiver was retuned.
func TestEnsureScanKeepsDecodedResult(t *testing.T) {
	t.Setenv("SDRSTREAM_STATE_DIR", t.TempDir())
	server := web.New(t.TempDir(), t.TempDir())

	scansMu.Lock()
	scans = nil
	scansMu.Unlock()

	decoded := web.ScanStatus{
		StationType: "KiwiSDR", Station: "kiwi.test",
		FrequencyHz: 5505000, Mode: "usb", State: "decoded",
		Result: "This is Shannon Volmet, wind 220 degrees, 10 knots.", UpdatedAt: time.Now(),
	}
	addScan(server, decoded)
	ensureScan(server, web.ScanStatus{
		StationType: "KiwiSDR", Station: "kiwi.test",
		FrequencyHz: 5505000, Mode: "usb", State: "queued", UpdatedAt: time.Now(),
	})

	scansMu.Lock()
	defer scansMu.Unlock()
	if len(scans) != 1 {
		t.Fatalf("scan rows = %d, want 1", len(scans))
	}
	if scans[0].State != "decoded" || !strings.Contains(scans[0].Result, "Shannon Volmet") {
		t.Fatalf("row = %+v, want the previous decoded transcript preserved", scans[0])
	}
}
