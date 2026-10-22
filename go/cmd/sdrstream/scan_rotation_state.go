package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// rotationState is the small amount of cross-process memory the fleet
// scanner needs: which Kiwi HF target a receiver processed last, and which
// OpenWebRX profile it should survey next. Without it a restart re-tunes the
// top of the HF list and the default profile forever, so the later entries
// of a big fleet would never be reached across sessions.
type rotationState struct {
	Version      int                 `json:"version"`
	KiwiCursor   map[string]string   `json:"kiwi_cursor,omitempty"`
	OpenProfiles map[string][]string `json:"openwebrx_profiles,omitempty"`
	OpenNext     map[string]int      `json:"openwebrx_next,omitempty"`
}

const rotationStateVersion = 1

// rotationStateFile is empty when persistence is disabled (tests and any
// caller that never calls configureRotationState). It is read by station
// goroutines, so access goes through the small lock below.
var (
	rotationStateMu   sync.RWMutex
	rotationStateFile string
	rotationPersistMu chan struct{}
)

func init() {
	rotationPersistMu = make(chan struct{}, 1)
	rotationPersistMu <- struct{}{}
}

func rotationStatePath() string {
	rotationStateMu.RLock()
	defer rotationStateMu.RUnlock()
	return rotationStateFile
}

func setRotationStatePath(path string) {
	rotationStateMu.Lock()
	rotationStateFile = path
	rotationStateMu.Unlock()
}

// defaultRotationStatePath follows the blocklist convention: a per-user
// state directory, overridable with SDRSTREAM_STATE_DIR.
func defaultRotationStatePath() string {
	dir := os.Getenv("SDRSTREAM_STATE_DIR")
	if dir == "" {
		dir = filepath.Join(os.Getenv("HOME"), "sdrstream-state")
	}
	return filepath.Join(dir, "scan-rotation.json")
}

// configureRotationState points the rotation cursors at path and loads any
// state a previous run left there. An empty path disables persistence, which
// is what tests want so they never touch the operator's state directory.
func configureRotationState(path string, logf func(string, ...interface{})) {
	setRotationStatePath(path)
	if path == "" {
		return
	}
	kiwi, profiles, next := loadRotationState(path)
	if len(kiwi) == 0 && len(profiles) == 0 {
		return
	}
	kiwiCursorMu.Lock()
	for host, target := range kiwi {
		if target != "" {
			kiwiCursorByHost[host] = target
		}
	}
	kiwiCursorMu.Unlock()
	openProfileMu.Lock()
	for host, names := range profiles {
		if len(names) > 0 {
			openProfilesByHost[host] = append([]string(nil), names...)
		}
	}
	for host, index := range next {
		openProfileNext[host] = index
	}
	openProfileMu.Unlock()
	if logf != nil {
		logf("scan: resumed rotation state for %d receiver(s) from %s", len(kiwi)+len(profiles), path)
	}
}

// loadRotationState returns the persisted maps, or empty maps when the file
// is missing, unreadable or written by a newer version. A corrupt cursor
// must never stop a scan; worst case it re-tunes the top of the list.
func loadRotationState(path string) (map[string]string, map[string][]string, map[string]int) {
	kiwi := map[string]string{}
	profiles := map[string][]string{}
	next := map[string]int{}
	data, err := os.ReadFile(path)
	if err != nil {
		return kiwi, profiles, next
	}
	var state rotationState
	if err := json.Unmarshal(data, &state); err != nil {
		return kiwi, profiles, next
	}
	if state.Version > rotationStateVersion {
		return kiwi, profiles, next
	}
	for host, target := range state.KiwiCursor {
		if target != "" {
			kiwi[host] = target
		}
	}
	for host, names := range state.OpenProfiles {
		if len(names) > 0 {
			profiles[host] = append([]string(nil), names...)
		}
	}
	for host, index := range state.OpenNext {
		if index >= 0 {
			next[host] = index
		}
	}
	return kiwi, profiles, next
}

// persistRotation snapshots both cursors under their own locks and writes
// the file atomically. It is called once per station visit (not per sample),
// so a synchronous write keeps the code simple and the state always matches
// what the scanner has actually processed.
func persistRotation() {
	path := rotationStatePath()
	if path == "" {
		return
	}
	<-rotationPersistMu
	defer func() { rotationPersistMu <- struct{}{} }()

	kiwiCursorMu.Lock()
	kiwi := make(map[string]string, len(kiwiCursorByHost))
	for host, target := range kiwiCursorByHost {
		kiwi[host] = target
	}
	kiwiCursorMu.Unlock()

	openProfileMu.Lock()
	profiles := make(map[string][]string, len(openProfilesByHost))
	for host, names := range openProfilesByHost {
		profiles[host] = append([]string(nil), names...)
	}
	next := make(map[string]int, len(openProfileNext))
	for host, index := range openProfileNext {
		next[host] = index
	}
	openProfileMu.Unlock()

	// Skip the write only when there is nothing to remember AND no stale
	// file to forget: a reset of the last receiver must still replace the
	// old state on disk, or a reload resurrects a deleted cursor.
	if len(kiwi) == 0 && len(profiles) == 0 && len(next) == 0 {
		if _, err := os.Stat(path); err != nil {
			return
		}
	}
	state := rotationState{
		Version:      rotationStateVersion,
		KiwiCursor:   kiwi,
		OpenProfiles: profiles,
		OpenNext:     next,
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return
	}
	data = append(data, '\n')
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return
		}
	}
	tmp := fmt.Sprintf("%s.tmp.%d", path, os.Getpid())
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		_ = os.Remove(tmp)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
	}
}
