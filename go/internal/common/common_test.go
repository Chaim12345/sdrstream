package common

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWriteWAVFileAtomic: the WAV lands complete and no temp file remains,
// so a directory scan never sees a half-written recording.
func TestWriteWAVFileAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clip.wav")
	if err := WriteWAVFile(path, []int16{100, -100, 200}, 8000); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 44+6 || string(data[:4]) != "RIFF" {
		t.Fatalf("unexpected WAV bytes: %d %q", len(data), data[:4])
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temp file left behind: %v", err)
	}
}

// TestWriteFileAtomicReplaces: a second write swaps the content whole.
func TestWriteFileAtomicReplaces(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := WriteFileAtomic(path, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(path, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "two" {
		t.Fatalf("content = %q, want two", data)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temp file left behind: %v", err)
	}
}
