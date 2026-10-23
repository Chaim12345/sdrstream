package catalogue

import (
	"math"
	"sort"
)

// ForFrequency returns receivers whose published bands cover frequencyHz:
// live-verified VHF/UHF receivers first (they actually answered and
// streamed), then the generated WebSDR table sorted by URL.
func ForFrequency(frequencyHz float64) []Receiver {
	out := ForFrequencyVerified(frequencyHz)
	seen := map[string]bool{}
	for _, receiver := range out {
		seen[receiver.URL] = true
	}
	var rest []Receiver
	for _, receiver := range Table {
		if receiver.CanHear(frequencyHz) && !seen[receiver.URL] {
			rest = append(rest, receiver)
		}
	}
	sort.Slice(rest, func(i, j int) bool { return rest[i].URL < rest[j].URL })
	return append(out, rest...)
}

// Nearest returns up to limit receivers that can hear frequencyHz, nearest to
// the position first. The distance is an equirectangular approximation,
// adequate at this scale.
func Nearest(latitude, longitude, frequencyHz float64, limit int) []Receiver {
	type keyed struct {
		receiver Receiver
		distance float64
	}
	var candidates []keyed
	for _, receiver := range Table {
		if !receiver.CanHear(frequencyHz) {
			continue
		}
		dLat := (receiver.Latitude - latitude) * (math.Pi / 180)
		dLon := (receiver.Longitude - longitude) * (math.Pi / 180)
		mean := (receiver.Latitude + latitude) / 2 * (math.Pi / 180)
		x := dLon * math.Cos(mean)
		candidates = append(candidates, keyed{receiver, math.Hypot(dLat, x)})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].distance < candidates[j].distance })
	out := make([]Receiver, 0, len(candidates))
	for _, candidate := range candidates {
		if len(out) >= limit {
			break
		}
		out = append(out, candidate.receiver)
	}
	return out
}
