package web

import (
	"strings"
	"testing"
)

// The activity/health/proof renderers must state provenance plainly:
// the reporting receiver is named as the origin ("via"), a missing
// source or receiver is stated, never guessed or silently omitted, and
// a zero-value fetch time reads "no successful fetch yet", never a
// bogus age. Behaviour-level coverage lives in scripts/ui_dom_test.mjs
// (real DOM-tree assertions); this guards the markers in CI.
func TestActivityProvenanceMarkers(t *testing.T) {
	b, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"source not recorded",
		"'via ' + shortURL(a.source)",
		"live gate passed: ",
		"receiver not recorded",
		"no successful fetch yet",
		"last successful fetch ",
		"no live data sources recorded since boot",
		"renderSourceFreshness",
		"verdictLabels",
	} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("app.js lacks %q", want)
		}
	}
	// The receiver name must never be framed as this server's own
	// reception: only the capture source-class model may say that.
	if strings.Contains(string(b), "activityProvenance(a) {\n  return a.source ? 'from ") {
		t.Fatal("activity provenance frames the receiver as the recording source")
	}
}
