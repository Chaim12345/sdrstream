package web

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// TestTranslationJS pins the translation display rules: text only for
// status "translated", reasons for every other status, unverified language.
func TestTranslationJS(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	script, _ := filepath.Abs("testdata/translation_test.js")
	asset, _ := filepath.Abs("static/translation.js")
	if out, err := exec.Command(node, script, asset).CombinedOutput(); err != nil {
		t.Fatalf("translation helper test failed: %v\n%s", err, out)
	}
}
