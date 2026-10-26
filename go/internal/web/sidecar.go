package web

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// SidecarIssue is one validation finding for a capture metadata sidecar.
type SidecarIssue struct {
	Level   string // "error" or "warning"
	Key     string // offending key, "" for file-level findings
	Message string
}

func (i SidecarIssue) String() string {
	if i.Key == "" {
		return fmt.Sprintf("%s: %s", i.Level, i.Message)
	}
	return fmt.Sprintf("%s: %s: %s", i.Level, i.Key, i.Message)
}

// sidecarKeys is the accepted key vocabulary, matching WriteCaptureMetadata
// and the sidecar parser in listCaptures.
var sidecarKeys = map[string]bool{
	"frequency_hz": true, "mode": true, "receiver": true, "decode": true,
	"kind": true, "target": true, "gate": true, "gate_verdict": true,
	"crosscheck": true, "crosscheck_ref": true, "crosscheck_detail": true,
	"started": true, "duration_sec": true, "verified": true, "source": true,
}

// ValidateSidecar checks one capture metadata sidecar (.txt next to a .wav)
// against the schema in docs/capture-sidecar.md. Errors break rendering or
// contradict another field; warnings are honesty lints a human should read.
func ValidateSidecar(data []byte) []SidecarIssue {
	var issues []SidecarIssue
	err := func(key, msg string) { issues = append(issues, SidecarIssue{"error", key, msg}) }
	warn := func(key, msg string) { issues = append(issues, SidecarIssue{"warning", key, msg}) }

	fields := map[string]string{}
	var keys []string
	for n, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			err("", fmt.Sprintf("line %d is not key=value: %q", n+1, line))
			continue
		}
		if !sidecarKeys[k] {
			err(k, "unknown key")
		}
		if _, dup := fields[k]; dup {
			err(k, "duplicate key")
		}
		fields[k] = v
		keys = append(keys, k)
	}
	_ = keys

	if v, ok := fields["frequency_hz"]; ok {
		f, ferr := strconv.ParseFloat(v, 64)
		if ferr != nil || f < 0 {
			err("frequency_hz", "must be a non-negative number, got "+strconv.Quote(v))
		}
	}
	if v, ok := fields["started"]; ok {
		if _, terr := time.Parse(time.RFC3339, v); terr != nil {
			err("started", "must be RFC3339 (e.g. 2026-10-23T07:48:00Z), got "+strconv.Quote(v))
		}
	}
	if v, ok := fields["duration_sec"]; ok {
		f, ferr := strconv.ParseFloat(v, 64)
		if ferr != nil || f < 0 {
			err("duration_sec", "must be a non-negative number, got "+strconv.Quote(v))
		}
	}
	switch v := fields["source"]; v {
	case "", "live", "test-fixture", "satnogs":
	default:
		warn("source", "unrecognised provenance class "+strconv.Quote(v)+`; the UI shows it verbatim (known: "live", "test-fixture", "satnogs")`)
	}
	if v, ok := fields["verified"]; ok && v != "true" && v != "false" {
		err("verified", `must be "true" or "false", got `+strconv.Quote(v))
	}
	switch v := fields["gate_verdict"]; v {
	case "", "passed", "failed":
	default:
		err("gate_verdict", `must be "passed" or "failed" when set, got `+strconv.Quote(v))
	}
	switch v := fields["crosscheck"]; v {
	case "", "verified", "mismatch":
	default:
		// Descriptive values render, but the reserved words carry a defined
		// meaning; anything else must read as a description, not a verdict.
		if strings.Contains(v, "verif") && !strings.Contains(v, "not") && !strings.Contains(v, "only") {
			warn("crosscheck", strconv.Quote(v)+` contains "verif" - reserve "verified" for an independent reference confirming the decode`)
		}
	}
	if fields["crosscheck"] == "verified" && fields["gate_verdict"] != "passed" {
		err("crosscheck", `"verified" (independent confirmation) requires gate_verdict=passed; verified live = gate passed AND cross-check verified`)
	}
	if fields["verified"] == "true" && fields["gate_verdict"] == "failed" {
		err("verified", "true contradicts gate_verdict=failed")
	}
	if fields["kind"] == "archive" && fields["gate_verdict"] == "passed" {
		warn("gate_verdict", "archive import claiming a passed live gate - archive imports did not run this server's live gate")
	}
	sort.SliceStable(issues, func(i, j int) bool { return issues[i].Level < issues[j].Level })
	return issues
}
