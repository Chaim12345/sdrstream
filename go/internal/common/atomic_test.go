package common

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
)

// Concurrent writers and readers on the same path: readers must never
// parse a torn file, and no temp file may linger afterwards.
func TestWriteFileAtomicConcurrentReaders(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.json")
	type manifest struct {
		Items []string `json:"items"`
	}
	var readers, writers sync.WaitGroup
	stop := make(chan struct{})
	readErrs := make(chan error, 8)
	for r := 0; r < 3; r++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				data, err := os.ReadFile(path)
				if err != nil {
					continue // absent between runs is fine
				}
				var m manifest
				if err := json.Unmarshal(data, &m); err != nil {
					select {
					case readErrs <- fmt.Errorf("torn read: %w", err):
					default:
					}
					return
				}
			}
		}()
	}
	for w := 0; w < 3; w++ {
		writers.Add(1)
		go func(w int) {
			defer writers.Done()
			// A large payload widens the torn-write window.
			items := make([]string, 400)
			for i := range items {
				items[i] = fmt.Sprintf("writer-%d-item-%d", w, i)
			}
			for n := 0; n < 40; n++ {
				data, _ := json.Marshal(manifest{Items: items})
				if err := WriteFileAtomic(path, data, 0o644); err != nil {
					t.Errorf("write: %v", err)
					return
				}
			}
		}(w)
	}
	writers.Wait()
	close(stop)
	readers.Wait()
	select {
	case err := <-readErrs:
		t.Fatal(err)
	default:
	}
	// The final file is valid and no temp files leaked.
	if _, err := os.ReadFile(path); err != nil {
		t.Fatal(err)
	}
	left, _ := filepath.Glob(filepath.Join(dir, ".tmp-*"))
	if len(left) > 0 {
		t.Errorf("temp files leaked: %v", left)
	}
}

// The requested perm flows through the process umask exactly like
// os.WriteFile: a restrictive umask tightens, and the helper never
// widens beyond what the caller's umask would have produced.
func TestWriteFileAtomicRespectsUmask(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	path := filepath.Join(t.TempDir(), "private.json")
	if err := WriteFileAtomic(path, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("perm under umask 077 = %o, want 600 (0644 tightened, not widened)", info.Mode().Perm())
	}
}

// Replacing an existing file takes the requested perm (masked by
// umask), like the previous os.WriteFile implementation: the old file's
// mode is not preserved across a rename.
func TestWriteFileAtomicReplaceUsesRequestedPerm(t *testing.T) {
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)
	path := filepath.Join(t.TempDir(), "index.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(path, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("perm = %o, want 644 (requested perm, umask 022)", info.Mode().Perm())
	}
}

// A symlink at the target path is replaced, never followed: the file it
// pointed at keeps its content and its mode.
func TestWriteFileAtomicDoesNotFollowSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(link, []byte("replacement"), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "original" {
		t.Errorf("symlink target overwritten: %q", data)
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Error("path is still a symlink; rename should have replaced it")
	}
	data, err = os.ReadFile(link)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "replacement" {
		t.Errorf("new content = %q", data)
	}
}
