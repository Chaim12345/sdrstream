package probe

// Import of an operator-fetched UberSDR public directory file into the
// station catalog. The directory is SELF-REPORTED by receiver operators
// (instance_reporter heartbeat in the upstream server); a listing is
// publication intent, not a verified service. Directory facts refresh
// the record's LISTING fields only - they never set Reachable and never
// overwrite an existing probe outcome. Fetching the file is a manual,
// on-demand act by the operator (ubersdr.FetchDirectory); there is no
// polling, no auto-refresh, and no merged public dataset here.

import (
	"fmt"
	"strings"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/ubersdr"
)

// DirectoryListingVerdict marks a catalog record that comes from a public
// directory listing alone. The UI must never read it as a probe result.
const DirectoryListingVerdict = "directory-listed (self-reported, not probed)"

// ImportUberSDRDirectory parses one UberSDR directory payload (as saved by
// a manual fetch) and upserts one Record per instance. fetchedAt is when
// the operator fetched the file; it stamps listing freshness, not a probe
// time. Returns the number of instances applied. A parse error aborts the
// import: a half-imported listing would misstate the directory.
func (c *Catalog) ImportUberSDRDirectory(raw []byte, fetchedAt time.Time) (int, error) {
	instances, err := ubersdr.ParseDirectory(raw)
	if err != nil {
		return 0, fmt.Errorf("probe: ubersdr directory import: %w", err)
	}
	n := 0
	for _, inst := range instances {
		url := inst.URL()
		rec := Record{
			Key:           strings.ToLower(strings.TrimPrefix(strings.TrimPrefix(url, "https://"), "http://")),
			Kind:          "ubersdr",
			Name:          inst.Label(),
			URL:           url,
			ListedUpdated: fetchedAt.UTC().Format(time.RFC3339),
			Verdict:       DirectoryListingVerdict,
			CheckedAt:     fetchedAt.UTC(),
		}
		c.directoryUpsert(rec)
		n++
	}
	if n > 0 {
		c.Touch(fetchedAt.UTC())
	}
	return n, nil
}

// directoryUpsert merges one directory listing without probe semantics:
// an existing probed record keeps its Reachable/Verdict/CheckedAt outcome
// and gains only the refreshed listing facts; a new record is added as
// listed-but-unprobed and never claims reachability.
func (c *Catalog) directoryUpsert(rec Record) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Records == nil {
		c.Records = map[string]*Record{}
	}
	if prev, ok := c.Records[rec.Key]; ok {
		prev.ListedUpdated = rec.ListedUpdated
		prev.ListedOffline = false // present in the online directory listing
		if prev.Verdict == "" {
			prev.Verdict = DirectoryListingVerdict
			prev.CheckedAt = rec.CheckedAt
		}
		if prev.Name == "" || prev.Name == rec.Key {
			prev.Name = rec.Name
		}
		if prev.URL == "" {
			prev.URL = rec.URL
		}
		if prev.Kind == "" {
			prev.Kind = rec.Kind
		}
		return
	}
	r := rec
	c.Records[rec.Key] = &r
}
