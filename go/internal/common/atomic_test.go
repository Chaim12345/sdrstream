package common

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
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
	// The write wins its requested permissions (CreateTemp is 0600).
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("perm = %v, want 644", info.Mode().Perm())
	}
}
