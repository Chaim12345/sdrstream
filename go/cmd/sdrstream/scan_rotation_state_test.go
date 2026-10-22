package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chaim12345/sdrstream/go/internal/decode"
)

// useRotationStateFile points persistence at a temp file and restores the
// disabled default when the test ends, so no test writes to the operator's
// SDRSTREAM_STATE_DIR.
func useRotationStateFile(t *testing.T, path string) {
	t.Helper()
	configureRotationState(path, nil)
	t.Cleanup(func() {
		setRotationStatePath("")
		resetRotationCursors()
	})
}

func TestRotationStateSurvivesRestart(t *testing.T) {
	resetRotationCursors()
	path := filepath.Join(t.TempDir(), "nested", "scan-rotation.json")
	useRotationStateFile(t, path)

	targets := []decode.HFTarget{{Name: "a"}, {Name: "b"}, {Name: "c"}}
	if got := nextKiwiTargets("kiwi.test:8073", targets); got[0].Name != "a" {
		t.Fatalf("first visit starts at %q, want a", got[0].Name)
	}
	noteKiwiProgress("kiwi.test:8073", []decode.HFResult{{Target: targets[1]}})
	noteOpenWebRXProfiles("owrx.test:8073", "2m", []string{"2m", "70cm", "HF"})

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("rotation state was not written: %v", err)
	}

	// Simulate a restart: the in-memory maps are gone, only the file remains.
	resetRotationCursors()
	configureRotationState(path, nil)

	if got := nextKiwiTargets("kiwi.test:8073", targets); got[0].Name != "c" {
		t.Fatalf("after restart the Kiwi visit starts at %q, want c (resume after b)", got[0].Name)
	}
	if got := nextOpenWebRXProfile("owrx.test:8073"); got != "70cm" {
		t.Fatalf("after restart the OpenWebRX profile = %q, want 70cm", got)
	}
}

func TestRotationResetIsPersisted(t *testing.T) {
	resetRotationCursors()
	path := filepath.Join(t.TempDir(), "scan-rotation.json")
	useRotationStateFile(t, path)

	noteOpenWebRXProfiles("owrx.test:8073", "2m", []string{"2m", "70cm"})
	resetOpenWebRXRotation("owrx.test:8073")

	resetRotationCursors()
	configureRotationState(path, nil)
	if got := nextOpenWebRXProfile("owrx.test:8073"); got != "" {
		t.Fatalf("profile after a persisted reset = %q, want the server default", got)
	}
}

func TestRotationStateIgnoresCorruptFile(t *testing.T) {
	resetRotationCursors()
	path := filepath.Join(t.TempDir(), "scan-rotation.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	useRotationStateFile(t, path)

	targets := []decode.HFTarget{{Name: "a"}, {Name: "b"}}
	if got := nextKiwiTargets("kiwi.test:8073", targets); got[0].Name != "a" {
		t.Fatalf("corrupt state should start from the top, got %q", got[0].Name)
	}
	// A corrupt file must not stop the next note from writing a clean one.
	noteKiwiProgress("kiwi.test:8073", []decode.HFResult{{Target: targets[0]}})
	kiwi, _, _ := loadRotationState(path)
	if kiwi["kiwi.test:8073"] != "a" {
		t.Fatalf("state after recovery = %v, want a", kiwi)
	}
}

func TestDefaultRotationStatePathHonoursStateDir(t *testing.T) {
	t.Setenv("SDRSTREAM_STATE_DIR", "/tmp/state-under-test")
	if got := defaultRotationStatePath(); got != filepath.Join("/tmp/state-under-test", "scan-rotation.json") {
		t.Fatalf("defaultRotationStatePath() = %q", got)
	}
}

func TestRotationStateDisabledWritesNothing(t *testing.T) {
	resetRotationCursors()
	setRotationStatePath("")
	noteKiwiProgress("kiwi.test:8073", []decode.HFResult{{Target: decode.HFTarget{Name: "a"}}})

	// No file is configured, so persistence is a no-op and the empty path
	// must not panic or invent state.
	kiwi, profiles, next := loadRotationState("")
	if len(kiwi) != 0 || len(profiles) != 0 || len(next) != 0 {
		t.Fatalf("empty path returned state: %v %v %v", kiwi, profiles, next)
	}
	// The in-memory cursor still advances, so a single run keeps rotating.
	targets := []decode.HFTarget{{Name: "a"}, {Name: "b"}}
	if got := nextKiwiTargets("kiwi.test:8073", targets); got[0].Name != "b" {
		t.Fatalf("in-memory rotation = %q, want b", got[0].Name)
	}
}
