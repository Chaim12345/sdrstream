package main

import (
	"errors"
	"fmt"
	"github.com/chaim12345/sdrstream/go/internal/web"
	"testing"
	"time"
)

func TestNewScanConfigCapsRampAndStartsAtConfiguredStep(t *testing.T) {
	cfg, err := newScanConfig(49, 100, "49,100", .15, .30, .70, .90, 20, 100, 40, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.start != 49 || cfg.maximum != 100 || len(cfg.ramp) != 2 || cfg.ramp[0] != 49 || cfg.ramp[1] != 100 {
		t.Fatalf("unexpected scan config: %+v", cfg)
	}
	if _, err := newScanConfig(101, 100, "20,50", .15, .30, .70, .90, 20, 100, 40, 5*time.Minute); err == nil {
		t.Fatal("start above maximum accepted")
	}
	if _, err := newScanConfig(20, 101, "20,50", .15, .30, .70, .90, 20, 100, 40, 5*time.Minute); err == nil {
		t.Fatal("maximum above hard cap accepted")
	}
}

func TestClassifyScanFailure(t *testing.T) {
	if got := classifyScanFailure(errors.New("connection refused")); !got.refused || got.timedOut {
		t.Fatalf("refusal classification: %+v", got)
	}
	if got := classifyScanFailure(errors.New("listen timeout")); !got.timedOut {
		t.Fatalf("timeout classification: %+v", got)
	}
}

func TestScanConfigTargetLimits(t *testing.T) {
	base := func(limit int, session time.Duration) error {
		_, err := newScanConfig(49, 100, "49,100", .15, .30, .70, .90, 20, 100, limit, session)
		return err
	}
	if err := base(40, 5*time.Minute); err != nil {
		t.Fatal(err)
	}
	if base(41, 5*time.Minute) == nil {
		t.Fatal("target limit above 40 accepted")
	}
	if base(40, 6*time.Minute) == nil {
		t.Fatal("station session above five minutes accepted")
	}
}

func TestScanStatusIndexesRemainStablePastDisplayWindow(t *testing.T) {
	scansMu.Lock()
	old := scans
	scans = nil
	scansMu.Unlock()
	defer func() { scansMu.Lock(); scans = old; scansMu.Unlock() }()
	server := web.New("", "")
	for i := 0; i < 600; i++ {
		addScan(server, web.ScanStatus{Station: fmt.Sprintf("rx-%d", i), FrequencyHz: 145500000, Mode: "fm", State: "connecting"})
	}
	setScanResult(server, "rx-599|145500000|fm", "timeout", "deadline")
	scansMu.Lock()
	got := scans[len(scans)-1]
	scansMu.Unlock()
	if got.Station != "rx-599" || got.State != "timeout" {
		t.Fatalf("last stable status = %+v", got)
	}
}

func TestRampUsesTimeoutsForAdvanceAndRefusalsForBackoff(t *testing.T) {
	cfg, err := newScanConfig(20, 100, "20,49,100", .15, .30, .70, .90, 20, 100, 40, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if got := nextRampStage(0, 3, 0, .20, .2, 2, cfg); got != 0 {
		t.Fatalf("advanced despite timeout/error rate above gate: %d", got)
	}
	if got := nextRampStage(0, 3, 0, .14, .2, 2, cfg); got != 1 {
		t.Fatalf("did not advance below configured error rate: %d", got)
	}
	if got := nextRampStage(2, 3, .31, .31, .2, 2, cfg); got != 1 {
		t.Fatalf("did not back off on excessive refusal rate: %d", got)
	}
	if got := nextRampStage(2, 3, .1, .1, .91, 2, cfg); got != 1 {
		t.Fatalf("did not back off on excessive load: %d", got)
	}
}
