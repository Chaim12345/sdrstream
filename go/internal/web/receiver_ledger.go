package web

// Receiver-capture proof ledger: every capture archived into the record-all
// directory (one WAV plus its sidecar) gets exactly one append-only entry in
// the shared proof ledger, tagged source="receiver". The sync is driven by
// the same directory listing that /api/metrics.json counts, so the number of
// receiver ledger entries equals the capture-success count in metrics: an
// archived clip is a successful capture whether or not the live quality gate
// verified its decode, and the entry records that verdict honestly.
//
// Entries carry the decode text, the engine label (sdrstream's own decoders,
// never a relayed provider), the receiver ID, the UTC recording time and the
// audio link back into the record-all archive, so the UI proof view can play
// the clip and show the raw decode for any ledger line.

import (
	"fmt"
	"strings"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/feeds"
)

// ReceiverLedgerSource tags proof-ledger entries recorded from this node's
// own receivers, as opposed to relayed internet feeds.
const ReceiverLedgerSource = "receiver"

// receiverLedgerEntry builds the proof-ledger record for one archived
// capture. ID is the archive filename, which is unique per clip, so a
// restarted process can recognise clips it already ledgered.
func receiverLedgerEntry(c Capture, fetchedAt time.Time) feeds.LedgerEntry {
	label := strings.TrimSpace(c.Target)
	if label == "" {
		label = strings.TrimSpace(c.Mode)
	}
	if label == "" {
		label = strings.TrimSpace(c.Kind)
	}
	if label == "" {
		label = "capture"
	}
	if c.FrequencyHz > 0 {
		label = fmt.Sprintf("%s %.3f MHz", label, c.FrequencyHz/1e6)
	}
	kind := strings.TrimSpace(c.Kind)
	if kind == "" {
		kind = "audio"
	}
	return feeds.LedgerEntry{
		Source:    ReceiverLedgerSource,
		Kind:      kind,
		ID:        c.Path,
		Label:     label,
		Detail:    c.Decode,
		ProviderT: c.Start.UTC(),
		FetchedAt: fetchedAt.UTC(),
		DecodedBy: feeds.DecodedByUs,
		Ref:       c.URL,
		Receiver:  c.Receiver,
	}
}

// syncReceiverLedger appends one proof-ledger entry per archived capture
// that does not have one yet. It must be called with a fresh directory
// listing (scanCaptures cache miss), never under s.mu.
func (s *Server) syncReceiverLedger(captures []Capture) {
	l := s.ledger()
	if l == nil {
		return
	}
	s.mu.Lock()
	if s.receiverLedgerSeen == nil {
		s.receiverLedgerSeen = map[string]bool{}
	}
	if !s.receiverLedgerLoaded {
		// Seed from the ledger so a restart does not re-ledger clips that
		// are still in the archive. The query cap bounds the remembered
		// window; older clips rotated out of the ledger could be re-added
		// once, which only duplicates history, never live data.
		for _, e := range l.Query(ReceiverLedgerSource, 5000) {
			if e.ID != "" {
				s.receiverLedgerSeen[e.ID] = true
			}
		}
		s.receiverLedgerLoaded = true
	}
	var fresh []Capture
	for _, c := range captures {
		if c.Path == "" || s.receiverLedgerSeen[c.Path] {
			continue
		}
		s.receiverLedgerSeen[c.Path] = true
		fresh = append(fresh, c)
	}
	s.mu.Unlock()
	now := time.Now().UTC()
	for _, c := range fresh {
		l.Append(receiverLedgerEntry(c, now))
	}
}
