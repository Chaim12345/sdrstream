package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/decode"
	"github.com/chaim12345/sdrstream/go/internal/monitor"
	"github.com/chaim12345/sdrstream/go/internal/verify"
)

// captureDecodeRequest names a recorded capture to decode into an image.
type captureDecodeRequest struct {
	Name string `json:"name"`
}

// captureDecodeResponse reports the decode outcome. A failed gate is a
// 200 with OK false: the request worked, the content honestly did not
// verify. HTTP errors are reserved for malformed requests and missing
// or indeterminate inputs.
type captureDecodeResponse struct {
	OK     bool   `json:"ok"`
	Reason string `json:"reason"`
}

// readCaptureSidecar reads the provenance sidecar of a recorded
// capture, the same key=value format WriteCaptureMetadata writes.
func readCaptureSidecar(dir, wavName string) (CaptureMetadata, error) {
	var meta CaptureMetadata
	data, err := os.ReadFile(filepath.Join(dir, strings.TrimSuffix(wavName, ".wav")+".txt"))
	if err != nil {
		return meta, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		switch {
		case strings.HasPrefix(line, "frequency_hz="):
			fmt.Sscanf(strings.TrimPrefix(line, "frequency_hz="), "%g", &meta.FrequencyHz)
		case strings.HasPrefix(line, "mode="):
			meta.Mode = strings.TrimPrefix(line, "mode=")
		case strings.HasPrefix(line, "receiver="):
			meta.Receiver = strings.TrimPrefix(line, "receiver=")
		case strings.HasPrefix(line, "decode="):
			meta.Detail = strings.TrimPrefix(line, "decode=")
		case strings.HasPrefix(line, "started="):
			if parsed, perr := time.Parse(time.RFC3339, strings.TrimPrefix(line, "started=")); perr == nil {
				meta.Started = parsed.UTC()
			}
		}
	}
	return meta, nil
}

// handleCaptureDecode decodes a recorded capture (sstv or wefax, from
// the sidecar's recorded mode) and publishes the image through the
// same gates and gallery path a live decode uses. The capture's own
// provenance anchors the gallery item: transmission start, frequency
// and receiver come from the sidecar, and decoded_from names the
// capture, so a user-triggered re-decode is never presented as a new
// reception. A decode that fails verification publishes nothing.
func (s *Server) handleCaptureDecode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	if !sameOriginPOST(r) {
		http.Error(w, "same-origin required", http.StatusForbidden)
		return
	}
	var req captureDecodeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(req.Name)
	if filepath.Base(name) != name || !strings.HasSuffix(strings.ToLower(name), ".wav") {
		http.Error(w, "name must be a bare capture .wav filename", http.StatusBadRequest)
		return
	}
	// Hold the capture against the retention pruner for the whole
	// read+decode+publish, same primitive the producers use: a
	// decoded_from pointer must never name a capture the pruner took
	// mid-decode.
	finish := BeginCapturePublish(s.capturesDir, name)
	defer finish()
	wavPath := filepath.Join(s.capturesDir, name)
	info, err := os.Stat(wavPath)
	if err != nil {
		http.Error(w, "capture not found", http.StatusNotFound)
		return
	}
	// Duplicate clicks are idempotent: an already-published re-decode
	// returns its recorded verdict instead of a second gallery item.
	s.mu.Lock()
	for _, it := range s.liveGallery {
		if it.DecodedFrom == name {
			s.mu.Unlock()
			writeJSON(w, captureDecodeResponse{OK: true, Reason: it.Verification + " (already published as " + it.Name + ")"})
			return
		}
	}
	s.mu.Unlock()
	meta, err := readCaptureSidecar(s.capturesDir, name)
	if err != nil {
		http.Error(w, "capture sidecar missing or unreadable: the recorded mode chooses the decoder, and guessing it would not be honest", http.StatusConflict)
		return
	}
	mode := strings.ToLower(strings.TrimSpace(meta.Mode))
	if mode != "sstv" && mode != "wefax" {
		http.Error(w, fmt.Sprintf("no image decoder for recorded mode %q (supported: sstv, wefax)", meta.Mode), http.StatusConflict)
		return
	}
	// Decodes are CPU-bound; run one at a time so a run of clicks
	// cannot pile up on a small device.
	s.decodeMu.Lock()
	defer s.decodeMu.Unlock()
	samples, rate, err := decode.ReadWAV(wavPath)
	if err != nil {
		http.Error(w, "capture audio unreadable: "+err.Error(), http.StatusUnprocessableEntity)
		return
	}
	if r.Context().Err() != nil {
		return // caller went away; do not spend the decode
	}
	started := meta.Started
	if started.IsZero() {
		started = info.ModTime().UTC()
	}
	activity := monitor.Activity{
		ID: int(time.Now().UnixNano()), Started: started,
		FrequencyHz: meta.FrequencyHz, Kind: mode, Mode: mode,
		Source: meta.Receiver,
	}
	var pngData []byte
	var reason string
	switch mode {
	case "sstv":
		data, rep, derr := decode.SSTVImageDetailed(samples, rate)
		if derr != nil || len(data) == 0 {
			writeJSON(w, captureDecodeResponse{OK: false, Reason: "sstv decode produced no image: " + fmt.Sprint(derr)})
			return
		}
		v := verify.SSTVImage("sstv", data, &rep)
		if !v.Live {
			writeJSON(w, captureDecodeResponse{OK: false, Reason: v.Reason})
			return
		}
		pngData, reason = data, v.Reason
		activity.SSTVReport = &rep
	case "wefax":
		data, rep, derr := decode.WEFAXImageDetailed(samples, rate, 120, 576)
		if derr != nil || len(data) == 0 {
			writeJSON(w, captureDecodeResponse{OK: false, Reason: "wefax decode produced no image: " + fmt.Sprint(derr)})
			return
		}
		v := verify.WEFAXImage("wefax", data, rep.SyncSource, rep.WedgeContrast, rep.DriftPPM, rep.DriftMinPPM, rep.DriftMaxPPM, rep.PeriodScore, rep.DeskewCorr)
		if !v.Live {
			writeJSON(w, captureDecodeResponse{OK: false, Reason: v.Reason})
			return
		}
		pngData, reason = data, v.Reason
		activity.WEFAXReport = &rep
	}
	if r.Context().Err() != nil {
		return // caller went away; do not publish a decode nobody sees
	}
	// The gallery shows what was verified: the gate's own diagnostics,
	// not the capture's recorded detail string.
	activity.Detail = reason
	if err := s.AddLiveArtifactDecoded(activity, pngData, name); err != nil {
		http.Error(w, "gallery publication rejected the decode: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, captureDecodeResponse{OK: true, Reason: reason})
}
