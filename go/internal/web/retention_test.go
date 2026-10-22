package web

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeCaptureFixture lays down a WAV plus optional sidecar body, sized and
// aged as requested, and returns the WAV name.
func writeCaptureFixture(t *testing.T, dir, name string, size int64, age time.Duration, sidecar string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-age)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if sidecar != "" {
		if err := os.WriteFile(filepath.Join(dir, name[:len(name)-4]+".txt"), []byte(sidecar), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return name
}

// TestPruneCaptures: retention frees space by deleting the oldest
// non-verified captures (with their sidecars) until under budget; verified
// captures are never pruned, and sidecars without a verified line count as
// unknown - prunable like any unverified capture.
func TestPruneCaptures(t *testing.T) {
	dir := t.TempDir()
	verified := writeCaptureFixture(t, dir, "VERIFIED_145000kHz_ft8_x.wav", 400, 72*time.Hour, "verified=true\n")
	oldest := writeCaptureFixture(t, dir, "OLD_145000kHz_voice_x.wav", 300, 96*time.Hour, "mode=voice\n")
	unknown := writeCaptureFixture(t, dir, "UNKNOWN_145000kHz_aprs_x.wav", 300, 48*time.Hour, "mode=aprs\nverified=false\n")
	newest := writeCaptureFixture(t, dir, "NEW_145000kHz_voice_x.wav", 300, time.Hour, "mode=voice\n")

	removed, freed := pruneCaptures(dir, 700)
	if removed != 2 || freed != 600 {
		t.Fatalf("pruneCaptures removed=%d freed=%d, want 2 and 600", removed, freed)
	}
	for _, gone := range []string{oldest, unknown} {
		if _, err := os.Stat(filepath.Join(dir, gone)); !os.IsNotExist(err) {
			t.Errorf("%s survived pruning", gone)
		}
		if _, err := os.Stat(filepath.Join(dir, gone[:len(gone)-4]+".txt")); !os.IsNotExist(err) {
			t.Errorf("%s sidecar survived pruning", gone)
		}
	}
	for _, kept := range []string{verified, newest} {
		if _, err := os.Stat(filepath.Join(dir, kept)); err != nil {
			t.Errorf("%s was pruned: %v", kept, err)
		}
	}

	// Under budget already: nothing happens.
	if removed, _ := pruneCaptures(dir, 1<<40); removed != 0 {
		t.Fatalf("pruned %d captures while under budget", removed)
	}

	// Only verified captures left, still over budget: they are kept.
	dir2 := t.TempDir()
	writeCaptureFixture(t, dir2, "KEEP_145000kHz_ft8_x.wav", 900, time.Hour, "verified=true\n")
	if removed, _ := pruneCaptures(dir2, 100); removed != 0 {
		t.Fatalf("pruned %d verified captures, want 0 (verified are never pruned first)", removed)
	}

	// No sidecar at all is unknown too.
	dir3 := t.TempDir()
	writeCaptureFixture(t, dir3, "BARE_145000kHz_voice_x.wav", 500, time.Hour, "")
	if removed, _ := pruneCaptures(dir3, 100); removed != 1 {
		t.Fatalf("sidecar-less capture: removed=%d, want 1 (unknown is prunable)", removed)
	}
}

// TestCapturesMaxBytesDefault: the retention budget defaults to a nonzero
// cap; a bogus override never disables it.
func TestCapturesMaxBytesDefault(t *testing.T) {
	t.Setenv("SDRSTREAM_CAPTURES_MAX_BYTES", "")
	if got := capturesMaxBytes(); got != defaultCapturesMaxBytes {
		t.Fatalf("default = %d, want %d", got, defaultCapturesMaxBytes)
	}
	t.Setenv("SDRSTREAM_CAPTURES_MAX_BYTES", "0")
	if got := capturesMaxBytes(); got != defaultCapturesMaxBytes {
		t.Fatalf("zero override = %d, want default %d (zero must not mean unbounded)", got, defaultCapturesMaxBytes)
	}
	t.Setenv("SDRSTREAM_CAPTURES_MAX_BYTES", "not-a-number")
	if got := capturesMaxBytes(); got != defaultCapturesMaxBytes {
		t.Fatalf("bogus override = %d, want default %d", got, defaultCapturesMaxBytes)
	}
	t.Setenv("SDRSTREAM_CAPTURES_MAX_BYTES", fmt.Sprintf("%d", 1<<20))
	if got := capturesMaxBytes(); got != 1<<20 {
		t.Fatalf("override = %d, want 1 MiB", got)
	}
}
