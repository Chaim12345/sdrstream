package probe

// Import of externally verified receiver test results (the bounded rxprobe
// batch ledgers) into the station catalog. The verdict string is carried
// VERBATIM from the ledger - "iq-samples-decoded", "iq-mode-stream-received-
// format-undecoded", "audio-frames-received", "dns-fail" - so the UI shows
// exactly what was proven, never an upgraded label.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// LedgerRow is one rxprobe attempt record.
type LedgerRow struct {
	Family    string `json:"family"`
	Endpoint  string `json:"endpoint"`
	StartedAt string `json:"started_at"`
	DialOK    bool   `json:"dial_ok"`
	Outcome   string `json:"outcome"`
	// URL is optional provenance from the candidate list (https endpoints
	// such as the UberSDR fleet); when absent the record falls back to
	// http://endpoint.
	URL string `json:"url,omitempty"`
}

// importKind maps a ledger family onto the catalog's receiver kinds.
func importKind(family string) string {
	switch strings.ToLower(strings.TrimSpace(family)) {
	case "kiwisdr":
		return "kiwi"
	case "openwebrx":
		return "openwebrx"
	case "websdr":
		return "websdr"
	case "spyserver":
		return "spyserver"
	case "ubersdr":
		return "ubersdr"
	case "web-888", "web888":
		return "web-888"
	default:
		return strings.ToLower(family)
	}
}

// recordURL keeps the candidate-list scheme when the ledger carries it so
// https receivers are not mislabeled http.
func recordURL(row LedgerRow) string {
	if u := strings.TrimSpace(row.URL); u != "" {
		return u
	}
	return "http://" + row.Endpoint
}

// importReachable reports whether the outcome proves the station answered.
// Negative transport outcomes (dns-fail, ws-fail, dial errors) are not.
func importReachable(r LedgerRow) bool {
	switch r.Outcome {
	case "dns-fail", "ws-fail", "dial-fail", "guard-rejected", "silent", "":
		return false
	}
	return r.DialOK
}

// ImportLedger reads an rxprobe JSONL ledger and upserts one Record per
// row. Returns the number of rows applied. Malformed rows abort the import:
// a half-imported test batch would misstate what was verified. updated_at
// is touched on success so the UI does not show "catalog updated unknown".
func (c *Catalog) ImportLedger(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	n := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var row LedgerRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return n, fmt.Errorf("probe: import %s: row %d: %w", path, n+1, err)
		}
		if row.Endpoint == "" || row.Family == "" {
			return n, fmt.Errorf("probe: import %s: row %d: missing family/endpoint", path, n+1)
		}
		rec := Record{
			Key:       strings.ToLower(row.Endpoint),
			Kind:      importKind(row.Family),
			Name:      row.Endpoint,
			URL:       recordURL(row),
			Reachable: importReachable(row),
			Verdict:   row.Outcome,
		}
		if t, err := time.Parse(time.RFC3339, row.StartedAt); err == nil {
			rec.CheckedAt = t
		} else {
			rec.CheckedAt = time.Now().UTC()
		}
		c.Update(rec)
		n++
	}
	if err := sc.Err(); err != nil {
		return n, err
	}
	if n > 0 {
		c.Touch(time.Now().UTC())
	}
	return n, nil
}
