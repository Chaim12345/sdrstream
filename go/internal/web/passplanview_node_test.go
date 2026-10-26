package web

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestPassplanViewJS(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	script, _ := filepath.Abs("testdata/passplanview_test.js")
	asset, _ := filepath.Abs("static/passplanview.js")
	if out, err := exec.Command(node, script, asset).CombinedOutput(); err != nil {
		t.Fatalf("planner UI: %v\n%s", err, out)
	}
}

func TestPlannerRefreshIndependentFromStatus(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	script, _ := filepath.Abs("testdata/planner_refresh_test.js")
	asset, _ := filepath.Abs("static/app.js")
	if out, err := exec.Command(node, script, asset).CombinedOutput(); err != nil {
		t.Fatalf("planner refresh: %v\n%s", err, out)
	}
}
