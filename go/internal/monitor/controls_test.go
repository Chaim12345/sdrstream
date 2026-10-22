package monitor

import "testing"

// TestLiveControlsOverrideOptions checks that a web control update wins
// over the Options the monitor was built with, and that unset knobs keep
// falling back to Options.
func TestLiveControlsOverrideOptions(t *testing.T) {
	m := New(Options{ThresholdDB: 8, StepHz: 25000, Mode: "auto"})
	if m.thresholdDB() != 8 || m.dialStepHz() != 25000 || m.listenMode() != "auto" {
		t.Fatalf("defaults: %v %v %v", m.thresholdDB(), m.dialStepHz(), m.listenMode())
	}
	m.SetControls(Controls{ThresholdDB: 14})
	if m.thresholdDB() != 14 {
		t.Fatalf("threshold = %v, want 14", m.thresholdDB())
	}
	if m.dialStepHz() != 25000 || m.listenMode() != "auto" {
		t.Fatalf("untouched knobs changed: %v %v", m.dialStepHz(), m.listenMode())
	}
	m.SetControls(Controls{StepHz: 5000, Mode: "nfm"})
	if m.dialStepHz() != 5000 || m.listenMode() != "nfm" {
		t.Fatalf("step/mode: %v %v", m.dialStepHz(), m.listenMode())
	}
	// Zero and empty never clobber a live value.
	m.SetControls(Controls{})
	if m.thresholdDB() != 14 || m.dialStepHz() != 5000 || m.listenMode() != "nfm" {
		t.Fatalf("empty update clobbered knobs: %v %v %v", m.thresholdDB(), m.dialStepHz(), m.listenMode())
	}
}
