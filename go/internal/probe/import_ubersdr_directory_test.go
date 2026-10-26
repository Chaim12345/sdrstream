package probe

import (
	"strings"
	"testing"
	"time"
)

// Two directory instances: one TLS with a callsign, one plain http.
const testUberSDRDirectory = `{
 "instances": [
  {"name": "K3FEF UberSDR", "callsign": "K3FEF", "location": "PA, USA",
   "host": "k3fef.example.com", "port": 8073, "tls": true,
   "max_clients": 8, "available_clients": 5,
   "public_iq_modes": ["iq48", "iq96"]},
  {"name": "Plain Host", "callsign": "", "location": "",
   "host": "plain.example.org", "port": 80, "tls": false,
   "max_clients": 2, "public_iq_modes": ["iq48"]},
  {"host": "no-modes.example.net", "port": 8073,
   "public_iq_modes": []}
 ]
}`

var testFetchTime = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

func TestImportUberSDRDirectoryAddsHonestListings(t *testing.T) {
	c := NewCatalog()
	n, err := c.ImportUberSDRDirectory([]byte(testUberSDRDirectory), testFetchTime)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	// The no-IQ-modes host is dropped by the directory parser itself.
	if n != 2 {
		t.Fatalf("applied %d, want 2", n)
	}
	rec := c.Records["k3fef.example.com:8073"]
	if rec == nil {
		t.Fatalf("missing k3fef record; keys: %v", catalogKeysOf(c))
	}
	if rec.Reachable {
		t.Error("directory listing must never set Reachable")
	}
	if rec.Verdict != DirectoryListingVerdict {
		t.Errorf("verdict = %q, want the honest listing marker", rec.Verdict)
	}
	if rec.Kind != "ubersdr" || rec.URL != "https://k3fef.example.com:8073" {
		t.Errorf("kind/url wrong: %q %q", rec.Kind, rec.URL)
	}
	if !strings.Contains(rec.Name, "K3FEF") {
		t.Errorf("name %q should carry the callsign", rec.Name)
	}
	if rec.ListedUpdated != "2026-10-26T12:00:00Z" {
		t.Errorf("listed_updated = %q", rec.ListedUpdated)
	}
	if rec.ConsecFailures != 0 {
		t.Error("a listing is not a failure observation; no failure count")
	}
}

func TestImportUberSDRDirectoryPreservesProbeOutcome(t *testing.T) {
	c := NewCatalog()
	// Existing probed record for the same endpoint.
	probed := Record{
		Key: "k3fef.example.com:8073", Kind: "ubersdr", Name: "k3fef.example.com:8073",
		URL: "https://k3fef.example.com:8073", Reachable: true,
		Verdict: "iq-samples-decoded", CheckedAt: testFetchTime.Add(-time.Hour),
	}
	c.importUpsert(probed)
	if _, err := c.ImportUberSDRDirectory([]byte(testUberSDRDirectory), testFetchTime); err != nil {
		t.Fatalf("import: %v", err)
	}
	rec := c.Records["k3fef.example.com:8073"]
	if !rec.Reachable || rec.Verdict != "iq-samples-decoded" {
		t.Errorf("probe outcome clobbered: reachable=%v verdict=%q", rec.Reachable, rec.Verdict)
	}
	if rec.CheckedAt != probed.CheckedAt {
		t.Errorf("probe time clobbered: %v", rec.CheckedAt)
	}
	if rec.ListedUpdated != "2026-10-26T12:00:00Z" {
		t.Errorf("listing freshness not refreshed: %q", rec.ListedUpdated)
	}
	if !strings.Contains(rec.Name, "K3FEF") {
		t.Errorf("operator label not picked up: %q", rec.Name)
	}
}

func TestImportUberSDRDirectoryAbortsOnBadPayload(t *testing.T) {
	c := NewCatalog()
	if _, err := c.ImportUberSDRDirectory([]byte("{not json"), testFetchTime); err == nil {
		t.Fatal("bad payload must abort, not half-import")
	}
	if len(c.Records) != 0 {
		t.Errorf("aborted import left %d records", len(c.Records))
	}
}

func catalogKeysOf(c *Catalog) []string {
	out := []string{}
	for k := range c.Records {
		out = append(out, k)
	}
	return out
}
