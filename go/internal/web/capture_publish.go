package web

import (
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
var capturePublishes = struct {
	sync.Mutex
	paths map[string]struct{}
}{paths: map[string]struct{}{}}

// BeginCapturePublish marks dir/name as being published by this
// process and returns the finish func. The caller MUST invoke finish
// when the sidecar is written or the publish fails; on failure the
// producer leaves the WAV in place (it becomes normally prunable as
// unknown under the unchanged retention policy - no new deletion).
func BeginCapturePublish(dir, wavName string) (finish func()) {
	key := filepath.Join(dir, wavName)
	capturePublishes.Lock()
	capturePublishes.paths[key] = struct{}{}
	capturePublishes.Unlock()
	return func() {
		capturePublishes.Lock()
		delete(capturePublishes.paths, key)
		capturePublishes.Unlock()
	}
}

func capturePublishInProgress(dir, wavName string) bool {
	capturePublishes.Lock()
	defer capturePublishes.Unlock()
	_, ok := capturePublishes.paths[filepath.Join(dir, wavName)]
	return ok
}
