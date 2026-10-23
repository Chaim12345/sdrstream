package web

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
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

// errVerifiedAtRemoval reports a prune attempt whose deletion-time
// recheck found the capture verified: its sidecar landed after the
// candidate scan read the directory.
var errVerifiedAtRemoval = errors.New("capture verified at removal time")

// removeCaptureIfNotPublishing deletes dir/name (WAV plus sidecar) only
// when no producer is publishing it and it is still unverified AT
// REMOVAL TIME, atomically with respect to BeginCapturePublish: the
// registry lock is held across both checks AND the removes.
//
// Two windows make the rechecks load-bearing. First, the prune
// candidate scan reads the sidecar and the registry as two independent
// observations: a producer finishing between them (sidecar written,
// registry cleared) would let a stale "unknown" verdict delete a
// verified capture. Second, the sidecar must go under the same lock as
// the WAV: removing it after lock release could delete a NEW verified
// sidecar from a same-name republish that started after the WAV
// removal, demoting verified content to prunable-unknown.
func removeCaptureIfNotPublishing(dir, wavName string) error {
	key := filepath.Join(dir, wavName)
	capturePublishes.Lock()
	defer capturePublishes.Unlock()
	if capturePublishes.paths[key] > 0 {
		return errPublishInProgress
	}
	if verified, _ := captureVerified(dir, wavName); verified {
		return errVerifiedAtRemoval
	}
	if err := os.Remove(key); err != nil {
		return err
	}
	// The sidecar goes with its recording; a missing sidecar is fine.
	_ = os.Remove(filepath.Join(dir, strings.TrimSuffix(wavName, ".wav")+".txt"))
	return nil
}
