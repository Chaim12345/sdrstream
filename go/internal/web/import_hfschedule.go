package web

// Import of hfschedule proof-ledger evidence. The hfschedule continuous
// branch (f4eaa48) performs real independent cross-checks for scheduled
// RTTY captures: an independent reference decoder (minimodem) re-decodes
// the same WAV and the capture is marked verified only when both engines
// agree. Its proof ledger records the two facets separately already -
// decode_live is the Go live gate's verdict, verified/ref_engine/
// verified_lines/verified_reason are the independent cross-check. The
// mapping below carries that evidence into the capture archive without
// inventing anything: a cross-check facet is set only when the archived
// entry explicitly records one.

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/feeds"
)

// HFScheduleProof is one entry of the hfschedule proof ledger, limited to
// the fields the capture archive imports.
type HFScheduleProof struct {
	Key            string  `json:"key"`
	UTCStarted     string  `json:"utc_started"`
	Name           string  `json:"name"`
	Kind           string  `json:"kind"`
	CarrierKHz     float64 `json:"carrier_khz"`
	Mode           string  `json:"mode"`
	Receiver       string  `json:"receiver"`
	Status         string  `json:"status"`
	DecodeLive     bool    `json:"decode_live"`
	DecodeReason   string  `json:"decode_reason"`
	SampleRate     int     `json:"sample_rate"`
	Samples        int     `json:"samples"`
	Verified       bool    `json:"verified"`
	VerifiedLines  int     `json:"verified_lines"`
	VerifiedReason string  `json:"verified_reason"`
	RefEngine      string  `json:"ref_engine"`
}

// CaptureMetadataFromHFSchedule maps an archived hfschedule proof entry to
// capture sidecar metadata. The legacy Verified flag keeps its documented
// meaning (the Go live gate's verdict, here decode_live); the independent
// cross-check lands only in the crosscheck facets, and only when the
// archived entry records that the reference engine actually ran.
func CaptureMetadataFromHFSchedule(p HFScheduleProof, decodeText string) (CaptureMetadata, error) {
	started, err := time.Parse(time.RFC3339, p.UTCStarted)
	if err != nil {
		return CaptureMetadata{}, fmt.Errorf("hfschedule proof %q: bad utc_started: %w", p.Key, err)
	}
	meta := CaptureMetadata{
		FrequencyHz: p.CarrierKHz * 1000,
		Mode:        p.Mode,
		Receiver:    p.Receiver,
		Detail:      decodeText,
		Kind:        p.Kind,
		Target:      p.Name,
		Gate:        p.DecodeReason,
		Started:     started.UTC(),
		Verified:    p.DecodeLive, // the Go live gate's verdict, not the cross-check
		GateVerdict: GateVerdictFor(p.DecodeLive),
	}
	if p.SampleRate > 0 && p.Samples > 0 {
		meta.DurationSec = float64(p.Samples) / float64(p.SampleRate)
	}
	// A cross-check is recorded only when the archived entry names the
	// reference engine that ran. verified without an engine, or an engine
	// without a verdict, is contradictory archived evidence and is
	// rejected rather than guessed.
	switch {
	case p.RefEngine == "" && (p.Verified || p.VerifiedReason != ""):
		return CaptureMetadata{}, fmt.Errorf("hfschedule proof %q: cross-check verdict without a reference engine", p.Key)
	case p.RefEngine != "" && p.Verified:
		meta.Crosscheck = feeds.CrosscheckVerified
	case p.RefEngine != "" && !p.Verified:
		meta.Crosscheck = feeds.CrosscheckMismatch
	}
	if meta.Crosscheck != "" {
		meta.CrosscheckRef = p.RefEngine
		meta.CrosscheckDetail = p.VerifiedReason
	}
	return meta, nil
}

// ImportHFScheduleProof writes the archive sidecar for one already-imported
// hfschedule WAV (name must be the bare WAV filename present in dir),
// carrying the two proof facets from the archived proof-ledger entry.
func ImportHFScheduleProof(dir, wavName string, p HFScheduleProof, decodeText string) error {
	meta, err := CaptureMetadataFromHFSchedule(p, decodeText)
	if err != nil {
		return err
	}
	if filepath.Base(wavName) != wavName {
		return fmt.Errorf("web: import name %q must be a bare filename", wavName)
	}
	return WriteCaptureMetadata(dir, wavName, meta)
}
