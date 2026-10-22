package hfschedule

import (
	"os"
	"strings"
	"testing"
)

func TestNormalizeRTTY(t *testing.T) {
	if got := normalizeRTTY("  cq cq\r\nDE\tddk2  "); got != "CQ CQ DE DDK2" {
		t.Errorf("got %q", got)
	}
}

func TestRTTYAgreementAnchorBothEngines(t *testing.T) {
	goText := "CQ CQ CQ DE DDK2 DDK2 FREQUENCIES 4583 KHZ 7646 KHZ 10100.8 KHZ"
	refText := "CQ CQ CQ DE DDK2 DDK2 FREQUENCIES 4583 KHZ 7646 KHZ 10100.8 KHZ"
	verified, lines, reason := RTTYAgreement(goText, refText, "DDK2")
	if !verified {
		t.Fatalf("identical decodes rejected: %s", reason)
	}
	if lines == 0 {
		t.Error("identical decodes share no agreeing lines")
	}
}

func TestRTTYAgreementNoisyButAgreeing(t *testing.T) {
	// Both engines recover the anchor; individual characters differ.
	goText := "CQ CQ CQ DE DDK2 DDK2 FREQXENCIES 4583 KHZ 7646 KHZ"
	refText := "CQ CQ CQ DE DDK2 DDK2 FREQUENCIES 4583 KHZ 76Z6 KHZ"
	verified, _, reason := RTTYAgreement(goText, refText, "DDK2")
	if !verified {
		t.Fatalf("agreeing noisy decodes rejected: %s", reason)
	}
}

func TestRTTYAgreementFalseTextFromCarrier(t *testing.T) {
	// The known failure mode: the Go decoder reads text out of a strong
	// carrier while the reference engine hears nothing decodable.
	verified, lines, reason := RTTYAgreement("CQ CQ CQ DE DDK2 DDK2 FREQUENCIES 4583 KHZ", "ryryryryry", "DDK2")
	if verified {
		t.Fatalf("false text verified: %d lines", lines)
	}
	if !strings.Contains(reason, "trivial") {
		t.Errorf("reason %q does not name the empty reference", reason)
	}
	verified, _, _ = RTTYAgreement("CQ CQ CQ DE DDK2 DDK2 FREQUENCIES 4583 KHZ", "", "DDK2")
	if verified {
		t.Fatal("empty reference verified")
	}
}

func TestRTTYAgreementEnginesDisagree(t *testing.T) {
	goText := "CQ CQ CQ DE DDK2 DDK2 FREQUENCIES 4583 KHZ"
	refText := "THE QUICK BROWN FOX JUMPS OVER THE LAZY DOG 0123456789"
	verified, _, reason := RTTYAgreement(goText, refText, "DDK2")
	if verified {
		t.Fatal("disagreeing engines verified")
	}
	if reason != "engines disagree" {
		t.Errorf("reason %q", reason)
	}
}

func TestRTTYAgreementAnchorMissingInReference(t *testing.T) {
	// The anchor is garbled in the reference but whole lines agree.
	goText := "CQ CQ CQ DE DDK2 DDK2 AMSTERDAM 06 12 20 28 BERLIN 05 11 19 27 WIEN 04 10 18 26"
	refText := "CQ CQ CQ DE DDX2 DDX2 AMSTERDAM 06 12 20 28 BERLIN 05 11 19 27 WIEN 04 10 18 26"
	verified, lines, reason := RTTYAgreement(goText, refText, "DDK2")
	if !verified || lines < 1 {
		t.Fatalf("line-agreement rejected: verified=%v lines=%d reason=%s", verified, lines, reason)
	}
}

func TestRTTYAgreementAnchorWithoutLineOverlap(t *testing.T) {
	// Anchor in both but bodies fully garbled: still verified, zero lines.
	verified, lines, _ := RTTYAgreement("DDK2 ###garble###", "###garble### DDK2", "DDK2")
	if !verified || lines != 0 {
		t.Errorf("verified=%v lines=%d", verified, lines)
	}
}

func TestReadLedgerReport(t *testing.T) {
	dir := t.TempDir()
	lines := []string{
		`{"key":"a","utc_started":"2026-10-22T20:00:00Z","utc_finished":"2026-10-22T20:05:00Z","kind":"rtty","status":"recorded","decode_live":true,"decode_reason":"anchor DDK2","verified":true,"verified_lines":12,"verified_reason":"anchor DDK2 in both engines, 12 agreeing lines","ref_engine":"minimodem"}`,
		`{"key":"b","utc_started":"2026-10-22T21:00:00Z","utc_finished":"2026-10-22T21:05:00Z","kind":"rtty","status":"recorded","decode_live":false,"decode_reason":"expected service anchor missing","verified_reason":"engines disagree"}`,
		`{"key":"c","utc_started":"2026-10-22T22:00:00Z","utc_finished":"2026-10-22T22:05:00Z","kind":"rtty","status":"failed","error":"connect timeout"}`,
		`torn tail line from a crash`,
	}
	writeLedger(t, dir, lines)
	rep, err := ReadLedgerReport(dir)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Attempts != 3 || rep.Recorded != 2 || rep.Failed != 1 {
		t.Errorf("attempts=%d recorded=%d failed=%d", rep.Attempts, rep.Recorded, rep.Failed)
	}
	if rep.VerifiedCaptures != 1 || rep.VerifiedLines != 12 {
		t.Errorf("verified captures=%d lines=%d", rep.VerifiedCaptures, rep.VerifiedLines)
	}
	if rep.Hours != 2.0833333333333335 && (rep.Hours < 2.0 || rep.Hours > 2.2) {
		t.Errorf("hours=%v", rep.Hours)
	}
	if rep.VerifiedLinesPer < 5.7 || rep.VerifiedLinesPer > 6.0 {
		t.Errorf("verified lines/hour=%v", rep.VerifiedLinesPer)
	}
	if rep.RejectReasons["engines disagree"] != 1 || rep.RejectReasons["failed: connect timeout"] != 1 {
		t.Errorf("reasons=%v", rep.RejectReasons)
	}
}

func TestReadLedgerReportMissing(t *testing.T) {
	rep, err := ReadLedgerReport(t.TempDir())
	if err != nil || rep.Attempts != 0 {
		t.Errorf("rep=%+v err=%v", rep, err)
	}
}

func writeLedger(t *testing.T, dir string, lines []string) {
	t.Helper()
	if err := os.WriteFile(dir+"/"+LedgerName, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}
