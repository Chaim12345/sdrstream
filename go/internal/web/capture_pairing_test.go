package web

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chaim12345/sdrstream/go/internal/voice"
)

// A recording named with an uppercase extension must pair with its
// sidecar exactly like a lowercase one: one capture, merged metadata,
// no audio-less duplicate card and no inflated capture count.
func TestUppercaseWAVPairsWithSidecar(t *testing.T) {
	dir := t.TempDir()
	samples := make([]float64, 4000)
	if err := voice.WriteWAV(filepath.Join(dir, "CAP_up.WAV"), samples, 8000); err != nil {
		t.Fatal(err)
	}
	if err := WriteCaptureMetadata(dir, "CAP_up.WAV", CaptureMetadata{Mode: "cw", Receiver: "simulated"}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, e := range entries {
		names[e.Name()] = true
	}
	if !names["CAP_up.txt"] {
		t.Fatalf("sidecar should be named CAP_up.txt, have %v", names)
	}
	s := &Server{capturesDir: dir}
	got := s.listCaptures()
	if len(got) != 1 {
		t.Fatalf("one recording listed %d captures: %v", len(got), got)
	}
	if got[0].Path != "CAP_up.WAV" || got[0].Mode != "cw" || got[0].Samples != 4000 {
		t.Fatalf("merged capture wrong: %+v", got[0])
	}
}

// A sidecar written before the case-insensitive pairing ("X.WAV.txt")
// still counts as the same recording's proof, never a second capture.
func TestLegacyUppercaseSidecarNotDoubleListed(t *testing.T) {
	dir := t.TempDir()
	samples := make([]float64, 4000)
	if err := voice.WriteWAV(filepath.Join(dir, "CAP_old.WAV"), samples, 8000); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "CAP_old.WAV.txt"), []byte("mode=cw\nreceiver=simulated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Server{capturesDir: dir}
	if got := s.listCaptures(); len(got) != 1 || got[0].Path != "CAP_old.WAV" {
		t.Fatalf("legacy uppercase sidecar double-listed: %v", got)
	}
}
