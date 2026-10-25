package probe

import "sort"

// Requirement describes what a decode or monitor assignment needs from a
// station. The zero value matches every reachable station.
type Requirement struct {
	// FreqHz, when non-zero, restricts the result to stations whose KNOWN
	// coverage includes this frequency. Stations with no coverage
	// information (WebSDR exposes none) are excluded: the fleet only
	// claims coverage it actually learned from a probe or listing.
	FreqHz int64
	// Kinds, when non-empty, restricts to these receiver kinds
	// (KindKiwi, KindOpenWebRX, KindWebSDR).
	Kinds []string
	// NoPassword excludes stations that advertise a password.
	NoPassword bool
}

// Select returns reachable stations satisfying req, best first: fewest
// consecutive failures, then freshest check. This is the assignment
// order - the best current bet for work that starts NOW. It differs from
// Reachable() (oldest check first), which is the reconsideration order a
// scheduler uses to refresh stale confidence; both are deliberate.
//
// Everything Select returns is probe- or listing-derived: a match says
// "this station answered and advertised this coverage", never "this
// station received anything".
func Select(snap Snapshot, req Requirement, limit int) []*Record {
	kindOK := func(k string) bool {
		if len(req.Kinds) == 0 {
			return true
		}
		for _, want := range req.Kinds {
			if k == want {
				return true
			}
		}
		return false
	}
	out := make([]*Record, 0, len(snap.Records))
	for _, r := range snap.Records {
		if !r.Reachable || !kindOK(r.Kind) {
			continue
		}
		if req.NoPassword && r.Capability.Passworded {
			continue
		}
		if req.FreqHz != 0 {
			c := r.Capability
			if c.FreqMaxHz == 0 && c.FreqMinHz == 0 {
				continue // coverage unknown: never claimed
			}
			if req.FreqHz < c.FreqMinHz || req.FreqHz > c.FreqMaxHz {
				continue
			}
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ConsecFailures != out[j].ConsecFailures {
			return out[i].ConsecFailures < out[j].ConsecFailures
		}
		if !out[i].CheckedAt.Equal(out[j].CheckedAt) {
			return out[i].CheckedAt.After(out[j].CheckedAt)
		}
		return out[i].Key < out[j].Key
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}
