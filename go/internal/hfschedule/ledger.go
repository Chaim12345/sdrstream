package hfschedule

// Proof ledger: one JSON line per completed capture attempt, appended to
// proof-ledger.jsonl in the capture directory. Every line carries the
// receiver, the UTC window, the honest status and the decode gate outcome,
// so a later report never has to guess which audio proved what.

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// LedgerName is the proof-ledger file inside the capture directory.
const LedgerName = "proof-ledger.jsonl"

// LedgerEntry is one proof record. Status is the capture outcome
// (recorded, silent or failed) and never implies decode validity;
// DecodeLive is the quality-gate verdict, DecodeReason its explanation.
type LedgerEntry struct {
	Key          string  `json:"key"`
	UTCStarted   string  `json:"utc_started"`
	UTCFinished  string  `json:"utc_finished"`
	Name         string  `json:"name"`
	Kind         string  `json:"kind"`
	CarrierKHz   float64 `json:"carrier_khz"`
	DialKHz      float64 `json:"dial_khz"`
	Mode         string  `json:"mode"`
	Receiver     string  `json:"receiver"`
	ReceiverType string  `json:"receiver_type"`
	Status       string  `json:"status"`
	DecodeLive   bool    `json:"decode_live"`
	DecodeReason string  `json:"decode_reason"`
	Error        string  `json:"error,omitempty"`
	WAV          string  `json:"wav,omitempty"`
	Text         string  `json:"text,omitempty"`
	PNG          string  `json:"png,omitempty"`
	SampleRate   int     `json:"sample_rate,omitempty"`
	Samples      int     `json:"samples,omitempty"`
	Peak         int     `json:"peak,omitempty"`
	RMS          float64 `json:"rms,omitempty"`
}

// LedgerEntryFrom flattens one audit into its proof-ledger record.
func LedgerEntryFrom(a Audit) LedgerEntry {
	return LedgerEntry{
		Key:          a.Occurrence.Key(),
		UTCStarted:   a.Started.UTC().Format("2006-01-02T15:04:05Z"),
		UTCFinished:  a.Finished.UTC().Format("2006-01-02T15:04:05Z"),
		Name:         a.Occurrence.Entry.Target.Name,
		Kind:         a.Occurrence.Entry.Target.Kind,
		CarrierKHz:   a.Occurrence.Entry.Target.CarrierKHz,
		DialKHz:      a.Occurrence.Entry.Target.DialKHz,
		Mode:         a.Occurrence.Entry.Target.Mode,
		Receiver:     a.Receiver.URL,
		ReceiverType: a.Receiver.Type,
		Status:       a.Status,
		DecodeLive:   a.DecodeLive,
		DecodeReason: a.DecodeReason,
		Error:        a.Error,
		WAV:          a.WAV,
		Text:         a.Text,
		PNG:          a.PNG,
		SampleRate:   a.SampleRate,
		Samples:      a.Samples,
		Peak:         a.Peak,
		RMS:          a.RMS,
	}
}

// AppendLedger writes one proof line. A disk error is returned so the
// caller stops rather than silently losing capture proof.
func AppendLedger(dir string, a Audit) error {
	b, err := json.Marshal(LedgerEntryFrom(a))
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, LedgerName), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(append(b, '\n'))
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	return cerr
}
