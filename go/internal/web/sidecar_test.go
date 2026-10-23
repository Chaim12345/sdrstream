package web

import (
	"testing"
)

func hasIssueAt(issues []SidecarIssue, level, key string) bool {
	for _, i := range issues {
		if i.Level == level && i.Key == key {
			return true
		}
	}
	return false
}

func TestValidateSidecarAcceptsWriterOutput(t *testing.T) {
	good := "frequency_hz=437550000\nmode=sstv\nreceiver=user-archive\ndecode=full frame\nkind=archive\ntarget=ISS\ngate=none (archive import)\nstarted=2026-10-23T07:48:00Z\nduration_sec=322.688\nverified=false\n"
	if issues := ValidateSidecar([]byte(good)); len(issues) != 0 {
		t.Fatalf("clean sidecar flagged: %v", issues)
	}
}

func TestValidateSidecarRejectsContradictions(t *testing.T) {
	cases := []struct {
		name, body, key string
	}{
		{"unknown key", "frequenc_hz=100\n", "frequenc_hz"},
		{"bad started", "started=yesterday\n", "started"},
		{"bad freq", "frequency_hz=lots\n", "frequency_hz"},
		{"bad duration", "duration_sec=-1\n", "duration_sec"},
		{"bad verified", "verified=maybe\n", "verified"},
		{"bad verdict", "gate_verdict=not-run\n", "gate_verdict"},
		{"crosscheck without gate", "crosscheck=verified\n", "crosscheck"},
		{"verified contradicts failed gate", "verified=true\ngate_verdict=failed\n", "verified"},
		{"duplicate", "mode=fm\nmode=usb\n", "mode"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !hasIssueAt(ValidateSidecar([]byte(c.body)), "error", c.key) {
				t.Errorf("no error for %s in %q", c.key, c.body)
			}
		})
	}
}

func TestValidateSidecarWarnings(t *testing.T) {
	if !hasIssueAt(ValidateSidecar([]byte("kind=archive\ngate_verdict=passed\n")), "warning", "gate_verdict") {
		t.Error("archive import claiming a passed live gate not warned")
	}
	if !hasIssueAt(ValidateSidecar([]byte("crosscheck=verified-by-replay\n")), "warning", "crosscheck") {
		t.Error("verif-flavoured descriptive crosscheck not warned")
	}
	if n := len(ValidateSidecar([]byte("crosscheck=visual-reproduction-only\n"))); n != 0 {
		t.Errorf("descriptive only-label flagged: %d issues", n)
	}
}
