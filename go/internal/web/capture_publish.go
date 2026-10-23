package web

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// capturePublishes tracks captures a producer in this process is
// actively publishing: WAV written, sidecar pending. pruneCaptures
// skips them, so a capture is never prunable mid-transaction.
//
// Scope is IN-PROCESS ONLY: producers in another process (a separate
// CLI run sharing the captures directory) are not tracked, and their
// post-rename, pre-sidecar window still reads as unknown to this
// pruner. Every producer that writes WAV-then-sidecar into a captures
// directory served by this process must go through BeginCapturePublish.
// paths is a REFCOUNT per capture: two overlapping publishes of the
// same filename (same-second captures) must not let the first finish
// expose the second's in-progress file.
var capturePublishes = struct {
	sync.Mutex
	paths map[string]int
}{paths: map[string]int{}}

// BeginCapturePublish marks dir/name as being published by this
// process and returns the finish func. The caller MUST invoke finish
// when the sidecar is written or the publish fails; on failure the
// producer leaves the WAV in place (it becomes normally prunable as
// unknown under the unchanged retention policy - no new deletion).
func BeginCapturePublish(dir, wavName string) (finish func()) {
	key := filepath.Join(dir, wavName)
	capturePublishes.Lock()
	capturePublishes.paths[key]++
	capturePublishes.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			capturePublishes.Lock()
			if capturePublishes.paths[key] > 1 {
				capturePublishes.paths[key]--
			} else {
				delete(capturePublishes.paths, key)
			}
			capturePublishes.Unlock()
		})
	}
}

func capturePublishInProgress(dir, wavName string) bool {
	capturePublishes.Lock()
	defer capturePublishes.Unlock()
	return capturePublishes.paths[filepath.Join(dir, wavName)] > 0
}

// errPublishInProgress reports a prune attempt on a tracked capture.
var errPublishInProgress = errors.New("capture publish in progress")

// removeCaptureIfNotPublishing deletes dir/name only when no producer
// is publishing it, atomically with respect to BeginCapturePublish:
// the registry lock is held across the check AND the remove, so a
// producer cannot register after the check and have its file deleted
// mid-publish (the check-then-remove TOCTOU window).
func removeCaptureIfNotPublishing(dir, wavName string) error {
	key := filepath.Join(dir, wavName)
	capturePublishes.Lock()
	defer capturePublishes.Unlock()
	if capturePublishes.paths[key] > 0 {
		return errPublishInProgress
	}
	return os.Remove(key)
}
