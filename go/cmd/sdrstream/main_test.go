package main

import (
	"errors"
	"testing"
	"time"
)

func TestNewScanConfigCapsRampAndStartsAtConfiguredStep(t *testing.T) {
	cfg, err := newScanConfig(49, 100, "20,50,100,200", .15, .30, .70, .90, 20, 100, 40, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.start != 49 || cfg.maximum != 100 || len(cfg.ramp) != 4 || cfg.ramp[0] != 20 || cfg.ramp[1] != 49 || cfg.ramp[2] != 50 || cfg.ramp[3] != 100 {
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
		_, err := newScanConfig(49, 100, "20,50,100,200", .15, .30, .70, .90, 20, 100, limit, session)
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
