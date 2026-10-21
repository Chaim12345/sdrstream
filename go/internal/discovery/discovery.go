// Package discovery finds satellites that the SatNOGS network is hearing but
// our curated table does not know about. The live observation feed shows new
// and lightly documented spacecraft before anyone writes them down; the
// SatNOGS convention marks unidentified objects with temporary NORAD ids in
// the 99xxx range.
package discovery

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/satellites"
	"github.com/chaim12345/sdrstream/go/internal/satnogs"
)

// SeenSatellite is a satellite observed by the network, with what was heard.
type SeenSatellite struct {
	NoradID      int
	Name         string
	Frequencies  []int64
	Modes        []string
	Observations int
	LastSeen     time.Time
	Known        bool
}

// DownlinkHz is the most frequently observed downlink, the likeliest one.
func (s SeenSatellite) DownlinkHz() (int64, bool) {
	if len(s.Frequencies) == 0 {
		return 0, false
	}
	counts := map[int64]int{}
	for _, frequency := range s.Frequencies {
		counts[frequency]++
	}
	best := s.Frequencies[0]
	bestCount := -1
	for frequency, count := range counts {
		if count > bestCount || (count == bestCount && frequency < best) {
			best, bestCount = frequency, count
		}
	}
	return best, true
}

// Summary renders one line for the report.
func (s SeenSatellite) Summary() string {
	frequency := "unknown"
	if downlink, ok := s.DownlinkHz(); ok {
		frequency = fmt.Sprintf("%.4f MHz", float64(downlink)/1e6)
	}
	modes := strings.Join(s.Modes, "/")
	if len(modes) > 24 {
		modes = modes[:24] + "…"
	}
	tag := ""
	if s.NoradID >= 99000 && s.NoradID < 100000 {
		tag = "  [undocumented: temporary 99xxx id]"
	}
	return fmt.Sprintf("%d  %s  %s  %s (%d obs)%s", s.NoradID, s.Name,
		frequency, modes, s.Observations, tag)
}

// Unknown returns the satellites from the recent feed that our table lacks.
func Unknown(limit int, status string, timeout time.Duration) ([]SeenSatellite, error) {
	observations, err := satnogs.Recent(limit, timeout)
	if err != nil {
		return nil, err
	}
	known := map[int]bool{}
	for _, sat := range satellites.Table {
		known[sat.NoradID] = true
	}
	byID := map[int]*SeenSatellite{}
	order := []int{}
	for _, observation := range observations {
		if status != "" && status != "all" && observation.Status != status {
			continue
		}
		if observation.NoradID == 0 {
			continue
		}
		entry, ok := byID[observation.NoradID]
		if !ok {
			entry = &SeenSatellite{NoradID: observation.NoradID, Name: observation.SatName}
			byID[observation.NoradID] = entry
			order = append(order, observation.NoradID)
		}
		if observation.FrequencyHz > 0 && !contains(entry.Frequencies, observation.FrequencyHz) {
			entry.Frequencies = append(entry.Frequencies, observation.FrequencyHz)
		}
		if observation.Mode != "" && !containsString(entry.Modes, observation.Mode) {
			entry.Modes = append(entry.Modes, observation.Mode)
		}
		entry.Observations++
		if observation.Start.After(entry.LastSeen) {
			entry.LastSeen = observation.Start
		}
	}
	out := make([]SeenSatellite, 0, len(order))
	for _, id := range order {
		entry := byID[id]
		entry.Known = known[entry.NoradID]
		if !entry.Known {
			out = append(out, *entry)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Observations > out[j].Observations })
	return out, nil
}

func contains(values []int64, value int64) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
