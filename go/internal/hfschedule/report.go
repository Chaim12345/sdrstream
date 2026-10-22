package hfschedule

// Run statistics from the proof ledger: verified lines per hour over the
// recorded window, plus the honest reject tallies. ReadLedgerReport never
// infers decode validity from capture status; it reports the cross-check
// fields as written.

import (
	"bufio"
	"encoding/json"
	"os"
	"sort"
	"time"
)

// Report summarises a proof ledger over its own UTC window.
type Report struct {
	From, To         time.Time
	Hours            float64
	Attempts         int
	Recorded         int
	Silent           int
	Failed           int
	VerifiedCaptures int            // cross-check verified (rtty only)
	VerifiedLines    int            // agreeing reference lines, all captures
	VerifiedLinesPer float64        // verified lines per wall-clock hour
	GateLive         int            // quality-gate passes (not cross-checked)
	RejectReasons    map[string]int // gate/capture reasons, non-verified
}

// ReadLedgerReport folds proof-ledger.jsonl into a Report. A missing file
// is an empty report, not an error.
func ReadLedgerReport(dir string) (Report, error) {
	f, err := os.Open(dir + "/" + LedgerName)
	if os.IsNotExist(err) {
		return Report{}, nil
	}
	if err != nil {
		return Report{}, err
	}
	defer f.Close()
	rep := Report{RejectReasons: map[string]int{}}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 65536), 1<<20)
	var reasons []string
	for sc.Scan() {
		var e LedgerEntry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			continue // a torn tail line from a crash is ignored, not fatal
		}
		rep.Attempts++
		started, serr := time.Parse("2006-01-02T15:04:05Z", e.UTCStarted)
		finished, ferr := time.Parse("2006-01-02T15:04:05Z", e.UTCFinished)
		if serr == nil && (rep.From.IsZero() || started.Before(rep.From)) {
			rep.From = started
		}
		if ferr == nil && finished.After(rep.To) {
			rep.To = finished
		}
		switch e.Status {
		case "recorded":
			rep.Recorded++
		case "silent":
			rep.Silent++
		case "failed":
			rep.Failed++
		}
		if e.DecodeLive {
			rep.GateLive++
		}
		if e.Verified {
			rep.VerifiedCaptures++
			rep.VerifiedLines += e.VerifiedLines
		} else if reason := e.VerifiedReason; reason != "" {
			reasons = append(reasons, reason)
		} else if e.DecodeReason != "" && e.Status == "recorded" {
			reasons = append(reasons, e.Status+": "+e.DecodeReason)
		} else if e.Error != "" {
			reasons = append(reasons, "failed: "+e.Error)
		}
	}
	if err := sc.Err(); err != nil {
		return rep, err
	}
	rep.Hours = rep.To.Sub(rep.From).Hours()
	if rep.Hours > 0 {
		rep.VerifiedLinesPer = float64(rep.VerifiedLines) / rep.Hours
	}
	sort.Strings(reasons)
	for _, r := range reasons {
		rep.RejectReasons[r]++
	}
	return rep, nil
}
