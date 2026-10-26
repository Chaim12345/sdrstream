package web

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// TestCoverageJS runs the receiver-coverage helper's node assertions. The
// helper is not wired into a view yet; this pins its overlap/unknown rules.
func TestCoverageJS(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	script, _ := filepath.Abs("testdata/coverage_test.js")
	asset, _ := filepath.Abs("static/coverage.js")
	if out, err := exec.Command(node, script, asset).CombinedOutput(); err != nil {
		t.Fatalf("coverage helper test failed: %v\n%s", err, out)
	}
}
