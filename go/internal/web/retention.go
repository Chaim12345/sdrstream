package web

import (
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// defaultCapturesMaxBytes bounds the captures directory. Recordings are
// written forever by the scanners, so without a cap the disk fills; 5 GiB
// holds weeks of short decodes while staying small for an SD card or a
// container volume. SDRSTREAM_CAPTURES_MAX_BYTES overrides it; zero or
// unparsable values fall back to the default, never to "unbounded".
const defaultCapturesMaxBytes = int64(5) << 30 // 5 GiB

// capturesMaxBytes is the configured capture budget in bytes.
func capturesMaxBytes() int64 {
	if raw := os.Getenv("SDRSTREAM_CAPTURES_MAX_BYTES"); raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil && n > 0 {
			return n
		}
		log.Printf("web: ignoring SDRSTREAM_CAPTURES_MAX_BYTES=%q (want a positive byte count)", raw)
	}
	return defaultCapturesMaxBytes
}

// captureVerified reports whether a capture's sidecar marks it verified.
// The second return value is false when the sidecar is missing, unreadable,
// or predates the verified line - such captures are "unknown", and
// retention treats them exactly like unverified ones.
func captureVerified(dir, wavName string) (verified, known bool) {
	data, err := os.ReadFile(filepath.Join(dir, strings.TrimSuffix(wavName, ".wav")+".txt"))
	if err != nil {
		return false, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if value, ok := strings.CutPrefix(line, "verified="); ok {
			return strings.TrimSpace(value) == "true", true
		}
	}
	return false, false
}

// pruneCaptures deletes captures (WAV plus sidecar) until the directory is
// within maxBytes. Unverified and unknown captures go first, oldest first;
// verified captures are never pruned, even when that leaves the directory
// over budget. Captures a producer in this process is actively publishing
// (WAV written, sidecar pending - see BeginCapturePublish) are skipped:
// that is producer coordination, not an eligibility change - any capture
// whose publish finished or failed is judged exactly as before. It
// returns how many captures were removed and how many bytes that freed.
func pruneCaptures(dir string, maxBytes int64) (removed int, freed int64) {
	if dir == "" || maxBytes <= 0 {
		return 0, 0
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0
	}
	type candidate struct {
		name    string
		size    int64
		modTime time.Time
	}
	var total int64
	var prunable []candidate
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || strings.HasPrefix(name, ".tmp-") || strings.HasSuffix(name, ".tmp") || !strings.HasSuffix(strings.ToLower(name), ".wav") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		total += info.Size()
		if verified, _ := captureVerified(dir, name); verified {
			continue
		}
		if capturePublishInProgress(dir, name) {
			// A producer in this process holds this capture between
			// its WAV and sidecar writes. Still counted in the total.
			continue
		}
		prunable = append(prunable, candidate{name: name, size: info.Size(), modTime: info.ModTime()})
	}
	if total <= maxBytes {
		return 0, 0
	}
	sort.Slice(prunable, func(i, j int) bool { return prunable[i].modTime.Before(prunable[j].modTime) })
	for _, item := range prunable {
		if total <= maxBytes {
			break
		}
		// Atomic recheck under the registry lock: a producer may have
		// begun publishing this name after the candidate scan, and its
		// sidecar may have landed (possibly verified) since the scan
		// read it. Both are re-judged at removal time; either outcome
		// skips the deletion.
		if err := removeCaptureIfNotPublishing(dir, item.name); err != nil {
			continue
		}
		total -= item.size
		removed++
		freed += item.size
	}
	return removed, freed
}

// retentionLoop prunes the captures directory at startup and then hourly.
func (s *Server) retentionLoop() {
	s.pruneCapturesOnce()
	for range time.Tick(time.Hour) {
		s.pruneCapturesOnce()
	}
}

func (s *Server) pruneCapturesOnce() {
	removed, freed := pruneCaptures(s.capturesDir, capturesMaxBytes())
	if removed > 0 {
		log.Printf("web: capture retention removed %d recording(s), freed %.1f MiB", removed, float64(freed)/(1<<20))
	}
}
