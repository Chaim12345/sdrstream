package catalogue

import (
	"math"
	"sort"
)

// ForFrequency returns receivers whose published bands cover frequencyHz,
// sorted by URL.
func ForFrequency(frequencyHz float64) []Receiver {
	var out []Receiver
	for _, receiver := range Table {
		if receiver.CanHear(frequencyHz) {
			out = append(out, receiver)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].URL < out[j].URL })
	return out
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
