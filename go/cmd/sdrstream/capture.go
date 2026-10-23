package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/decode"
	"github.com/chaim12345/sdrstream/go/internal/monitor"
	"github.com/chaim12345/sdrstream/go/internal/web"
)

// captureMeta is the provenance attached to one archived clip. It is
// deliberately independent of the UI so a fleet scan can record a clip
// before (or without) deciding whether it belongs in the gallery.
type captureMeta struct {
	Kind        string
	Mode        string
	Station     string
	FrequencyHz float64
	Started     time.Time
	Detail      string
	Gate        string
	Target      string
	DurationSec float64
	Verified    bool
	// Crosscheck / CrosscheckRef stay empty until a capture path performs
	// an independent reference check.
	Crosscheck    string
	CrosscheckRef string
}

// writeCaptureWAV stores one already-encoded WAV plus its sidecar. Every
// scan path uses this so a record-all archive has the same shape whether
// the clip came from OpenWebRX, WebSDR or a KiwiSDR.
func writeCaptureWAV(capturesDir string, wav []byte, meta captureMeta) (string, error) {
	if strings.TrimSpace(capturesDir) == "" {
		return "", fmt.Errorf("capture: no captures directory")
	}
	if len(wav) == 0 {
		return "", fmt.Errorf("capture: empty WAV")
	}
	if meta.Started.IsZero() {
		meta.Started = time.Now().UTC()
	}
	if meta.DurationSec <= 0 {
		meta.DurationSec = wavDurationSec(wav)
	}
	name := captureFileName(meta)
	if err := os.MkdirAll(capturesDir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(capturesDir, name)
	if err := os.WriteFile(path, wav, 0o644); err != nil {
		return "", err
	}
	sidecarErr := web.WriteCaptureMetadata(capturesDir, name, web.CaptureMetadata{
		FrequencyHz:   meta.FrequencyHz,
		Mode:          meta.Mode,
		Receiver:      meta.Station,
		Detail:        meta.Detail,
		Kind:          meta.Kind,
		Target:        meta.Target,
		Gate:          meta.Gate,
		Started:       meta.Started,
		DurationSec:   meta.DurationSec,
		Verified:      meta.Verified,
		GateVerdict:   web.GateVerdictFor(meta.Verified),
		Crosscheck:    meta.Crosscheck,
		CrosscheckRef: meta.CrosscheckRef,
	})
	return path, sidecarErr
}

// writeCaptureSamples converts scanner samples to a 16-bit mono WAV and
// archives it. It is used by the sweep/fixed-target paths, which already
// hold decoded audio in memory.
func writeCaptureSamples(capturesDir string, samples []float64, rate int, meta captureMeta) (string, error) {
	if rate <= 0 || len(samples) == 0 {
		return "", fmt.Errorf("capture: no audio")
	}
	pcm := make([]int16, len(samples))
	for i, sample := range samples {
		switch {
		case sample > 1:
			sample = 1
		case sample < -1:
			sample = -1
		}
		pcm[i] = int16(sample * 32767)
	}
	meta.DurationSec = float64(len(samples)) / float64(rate)
	return writeCaptureWAV(capturesDir, decode.WriteWAV16(pcm, rate), meta)
}

// captureFileName gives every clip a unique, sortable name. The station is
// part of the name because each fleet monitor numbers its own activities
// from 1 and two receivers can hear the same frequency simultaneously.
func captureFileName(meta captureMeta) string {
	kind := strings.ToLower(strings.TrimSpace(meta.Kind))
	if kind == "" {
		kind = "unknown"
	}
	mode := captureToken(meta.Mode)
	if mode == "" {
		mode = "audio"
	}
	station := captureToken(captureHost(meta.Station))
	if station == "" {
		station = "receiver"
	}
	started := meta.Started.UTC()
	if started.IsZero() {
		started = time.Now().UTC()
	}
	return fmt.Sprintf("%s_%dkHz_%s_%s_%s_%d.wav",
		strings.ToUpper(kind), int(meta.FrequencyHz/1000), mode, station,
		started.Format("20060102T150405Z"), time.Now().UnixNano())
}

// captureHost reduces a station URL to host[:port] for filenames.
func captureHost(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Host
	}
	if i := strings.IndexByte(raw, '/'); i >= 0 {
		return raw[:i]
	}
	return raw
}

// captureToken keeps filenames portable: only letters, digits, '-' and '_'.
func captureToken(value string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, strings.TrimSpace(value))
}

// wavDurationSec reads the duration from a canonical 16-bit mono WAV.
func wavDurationSec(wav []byte) float64 {
	if len(wav) < 44 {
		return 0
	}
	rate := binary.LittleEndian.Uint32(wav[24:28])
	data := binary.LittleEndian.Uint32(wav[40:44])
	if rate == 0 {
		return 0
	}
	return float64(data) / float64(rate*2)
}

// writeActivitySidecar annotates an already-archived monitor capture. The
// monitor writes the WAV; this adds the station, kind and gate verdict that
// make the archive useful after the scan has moved on.
func writeActivitySidecar(server *web.Server, activity monitor.Activity, verified bool, gate string) {
	if server == nil || strings.TrimSpace(activity.AudioPath) == "" {
		return
	}
	_ = server.AddCaptureMetadataFull(filepath.Base(activity.AudioPath), web.CaptureMetadata{
		FrequencyHz: activity.FrequencyHz,
		Mode:        activity.Mode,
		Receiver:    activity.Source,
		Detail:      activity.Detail,
		Kind:        activity.Kind,
		Target:      activity.Target,
		Gate:        gate,
		Started:     activity.Started,
		DurationSec: activity.DurationSec,
		Verified:    verified,
		GateVerdict: web.GateVerdictFor(verified),
	})
}

// saveHFResultCapture archives a Kiwi/WebSDR HF result. With recordAll it
// keeps unverified and non-decodable clips too; otherwise only verified
// results are written. An empty path with a nil error means "nothing to
// save", not a failure.
func saveHFResultCapture(capturesDir, host string, result decode.HFResult, recordAll bool) (string, error) {
	if strings.TrimSpace(capturesDir) == "" || len(result.AudioWAV) == 0 {
		return "", nil
	}
	if !recordAll && !result.Live() {
		return "", nil
	}
	kind := "data"
	switch result.Target.Kind {
	case "voice":
		kind = "voice"
	case "wefax", "sstv", "apt", "eas":
		kind = result.Target.Kind
	}
	return writeCaptureWAV(capturesDir, result.AudioWAV, captureMeta{
		Kind:        kind,
		Mode:        result.Target.Kind,
		Station:     host,
		FrequencyHz: result.Target.DialKHz * 1000,
		Started:     result.Started,
		Detail:      result.Text,
		Gate:        result.Gate.Reason,
		Target:      result.Target.Name,
		DurationSec: wavDurationSec(result.AudioWAV),
		Verified:    result.Live(),
	})
}

// pruneCaptures deletes the oldest WAVs (and their sidecars) until the
// directory is inside both limits. A zero limit disables that dimension.
// It returns the number of WAVs removed and the bytes freed.
func pruneCaptures(dir string, maxBytes int64, maxAge time.Duration, now time.Time) (int, int64, error) {
	return pruneCapturesWith(dir, maxBytes, maxAge, now, os.Remove)
}

// pruneCapturesWith is pruneCaptures with the file removal injected, so
// tests can exercise the failed-removal path (a file held open elsewhere,
// a wedged FUSE mount) without root tricks.
func pruneCapturesWith(dir string, maxBytes int64, maxAge time.Duration, now time.Time, removeFile func(path string) error) (int, int64, error) {
	if strings.TrimSpace(dir) == "" || (maxBytes <= 0 && maxAge <= 0) {
		return 0, 0, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0, err
	}
	type item struct {
		name string
		size int64
		mod  time.Time
	}
	var files []item
	var total int64
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".wav") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		files = append(files, item{name: entry.Name(), size: info.Size(), mod: info.ModTime()})
		total += info.Size()
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	removed := 0
	var freed int64
	remove := func(file item) {
		path := filepath.Join(dir, file.name)
		if err := removeFile(path); err != nil {
			// The file survived; keep its bytes in the accounting or the
			// size loop stops early and a full disk never drains.
			return
		}
		_ = os.Remove(filepath.Join(dir, strings.TrimSuffix(file.name, ".wav")+".txt"))
		total -= file.size
		removed++
		freed += file.size
	}
	if maxAge > 0 {
		for _, file := range files {
			if now.Sub(file.mod) <= maxAge {
				continue
			}
			remove(file)
		}
	}
	if maxBytes > 0 {
		for _, file := range files {
			if total <= maxBytes {
				break
			}
			if _, err := os.Stat(filepath.Join(dir, file.name)); err != nil {
				continue
			}
			remove(file)
		}
	}
	return removed, freed, nil
}

// pruneCapturesLoop runs retention while the web scanner is up. The default
// is no pruning; enabling either limit makes continuous record-all scans
// sustainable on a small disk.
func pruneCapturesLoop(ctx context.Context, dir string, maxBytes int64, maxAge, every time.Duration, logf func(string, ...interface{})) {
	if maxBytes <= 0 && maxAge <= 0 {
		return
	}
	if every <= 0 {
		every = time.Minute
	}
	timer := time.NewTimer(every)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			removed, freed, err := pruneCaptures(dir, maxBytes, maxAge, time.Now())
			if err != nil {
				logf("captures: prune: %v", err)
			} else if removed > 0 {
				logf("captures: pruned %d clip(s), freed %.1f MiB", removed, float64(freed)/(1<<20))
			}
			timer.Reset(every)
		}
	}
}
