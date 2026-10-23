package web

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Real archived hfschedule proof-ledger entries (run1 archive, DWD
// Pinneberg RTTY, 2026-10-23): the hfschedule continuous branch (f4eaa48)
// ran minimodem as an independent reference engine against the same WAV
// and recorded the outcome explicitly. The Go decode and the reference
// text both recover the DDK2 header and the 4583/7646/10100.8 kHz
// frequency line from the same audio.
const realHFScheduleEntries = `{"key":"e44edfd4e58efaff-20261023T0205Z","utc_started":"2026-10-23T02:05:09Z","utc_finished":"2026-10-23T02:09:10Z","name":"DWD 1 Pinneberg RTTY","kind":"rtty","carrier_khz":4583,"dial_khz":4581.1,"mode":"usb","receiver":"kiwisdr.johnnorton.co.uk:8073","receiver_type":"kiwi","status":"recorded","decode_live":true,"decode_reason":"anchor DDK2","sample_rate":11999,"samples":2881536,"verified":true,"verified_lines":9,"verified_reason":"anchor DDK2 in both engines, 9 agreeing lines","ref_engine":"minimodem"}
{"key":"1fbcbaac64acbb58-20261023T0305Z","utc_started":"2026-10-23T03:05:07Z","utc_finished":"2026-10-23T03:09:07Z","name":"DWD 1 Pinneberg RTTY","kind":"rtty","carrier_khz":4583,"dial_khz":4581.1,"mode":"usb","receiver":"http://websdr.ewi.utwente.nl:8901","receiver_type":"websdr","status":"recorded","decode_live":true,"decode_reason":"anchor DDK2","sample_rate":7119,"samples":1707776,"verified":true,"verified_lines":8,"verified_reason":"anchor DDK2 in both engines, 8 agreeing lines","ref_engine":"minimodem"}`

func TestImportHFScheduleRealCrossVerifiedEntries(t *testing.T) {
	dir := t.TempDir()
	for i, line := range strings.Split(realHFScheduleEntries, "\n") {
		var p HFScheduleProof
		if err := json.Unmarshal([]byte(line), &p); err != nil {
			t.Fatal(err)
		}
		meta, err := CaptureMetadataFromHFSchedule(p, "CQ CQ CQ DE DDK2 DDH7 DDK9")
		if err != nil {
			t.Fatalf("entry %d: %v", i, err)
		}
		// The two facets stay separate and both come from the archive:
		// the gate facet from decode_live, the cross-check facet from
		// verified+ref_engine. Nothing is inferred.
		if !meta.Verified || meta.GateVerdict != "passed" || meta.Gate != "anchor DDK2" {
			t.Fatalf("entry %d: gate facet wrong: %+v", i, meta)
		}
		if meta.Crosscheck != "verified" || meta.CrosscheckRef != "minimodem" {
			t.Fatalf("entry %d: cross-check facet wrong: %+v", i, meta)
		}
		if !strings.Contains(meta.CrosscheckDetail, "agreeing lines") {
			t.Fatalf("entry %d: archived checker words dropped: %q", i, meta.CrosscheckDetail)
		}
		wantDur := float64(p.Samples) / float64(p.SampleRate)
		if meta.DurationSec < wantDur-0.01 || meta.DurationSec > wantDur+0.01 {
			t.Fatalf("entry %d: duration %v, want %v", i, meta.DurationSec, wantDur)
		}

		// Full import writes a sidecar the capture listing reads back with
		// the same facets.
		name := p.Key + ".wav"
		if err := ImportHFScheduleProof(dir, name, p, "CQ CQ CQ DE DDK2 DDH7 DDK9"); err != nil {
			t.Fatalf("entry %d import: %v", i, err)
		}
		raw, err := os.ReadFile(filepath.Join(dir, p.Key+".txt"))
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"gate_verdict=passed", "crosscheck=verified", "crosscheck_ref=minimodem"} {
			if !strings.Contains(string(raw), key) {
				t.Fatalf("entry %d sidecar missing %q:\n%s", i, key, raw)
			}
		}
	}
}

// Import never invents a cross-check: no reference engine recorded means
// no cross-check facet, and contradictory archived evidence (a verdict
// with no engine) is an error, not a guess.
func TestImportHFScheduleNeverInventsCrosscheck(t *testing.T) {
	plain := HFScheduleProof{Key: "k1", UTCStarted: "2026-10-23T00:00:00Z",
		Name: "NAVTEX", Kind: "navtex", DecodeLive: false, DecodeReason: "gate-incomplete"}
	meta, err := CaptureMetadataFromHFSchedule(plain, "text")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Crosscheck != "" || meta.CrosscheckRef != "" {
		t.Fatalf("cross-check invented from nothing: %+v", meta)
	}
	if meta.GateVerdict != "failed" || meta.Verified {
		t.Fatalf("gate facet wrong: %+v", meta)
	}

	contradictory := plain
	contradictory.Verified = true
	contradictory.VerifiedReason = "claims agreement with no engine"
	if _, err := CaptureMetadataFromHFSchedule(contradictory, "text"); err == nil {
		t.Fatal("verdict without a reference engine must be rejected")
	}

	disagree := plain
	disagree.RefEngine = "minimodem"
	disagree.VerifiedReason = "engines disagree"
	meta, err = CaptureMetadataFromHFSchedule(disagree, "text")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Crosscheck != "mismatch" {
		t.Fatalf("ran-but-disagreed must map to mismatch, got %q", meta.Crosscheck)
	}
}

// hfschedule-style archives keep raw decode text in <key>.txt and the
// reference decode in <key>.ref.txt. Neither is a key=value sidecar, so a
// captures directory containing them must not grow phantom text-only
// proof entries.
func TestSidecarOnlyListingIgnoresRawTextFiles(t *testing.T) {
	dir := t.TempDir()
	raw := "CQ CQ CQ DE DDK2 DDH7 DDK9\nFREQUENCIES   4583 KHZ   7646 KHZ   10100.8 KHZ\n"
	for _, name := range []string{"e44edfd4e58efaff-20261023T0205Z.txt", "e44edfd4e58efaff-20261023T0205Z.ref.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(raw), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{capturesDir: dir}
	if got := s.listCaptures(); len(got) != 0 {
		t.Fatalf("raw text files mis-listed as proof: %+v", got)
	}
}
