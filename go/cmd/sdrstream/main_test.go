package main

import (
	"errors"
	"testing"
)

func TestNewScanConfigCapsRampAndStartsAtConfiguredStep(t *testing.T) {
	cfg, err := newScanConfig(20, 80, "20,50,100,200", .15, .30, .70, .90, 20, 100)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.start != 20 || cfg.maximum != 80 || len(cfg.ramp) != 3 || cfg.ramp[0] != 20 || cfg.ramp[1] != 50 || cfg.ramp[2] != 80 {
		t.Fatalf("unexpected scan config: %+v", cfg)
	}
	if _, err := newScanConfig(81, 80, "20,50", .15, .30, .70, .90, 20, 100); err == nil {
		t.Fatal("start above maximum accepted")
	}
	if _, err := newScanConfig(20, 81, "20,50", .15, .30, .70, .90, 20, 100); err == nil {
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
