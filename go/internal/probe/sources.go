package probe

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/directory"
	"github.com/chaim12345/sdrstream/go/internal/kiwisdr"
)

// OpenDirectorySource adapts the receiverbook listing (OpenWebRX + WebSDR
// receivers) into sweep records.
func OpenDirectorySource(ctx context.Context) ([]Record, error) {
	stations, err := directory.FetchStations(ctx, 30*time.Second)
	if err != nil {
		return nil, fmt.Errorf("fetch receiver directory: %w", err)
	}
	out := make([]Record, 0, len(stations))
	for _, st := range stations {
		var kind string
		switch st.Type {
		case "OpenWebRX":
			kind = KindOpenWebRX
		case "WebSDR":
			kind = KindWebSDR
		default:
			continue
		}
		out = append(out, Record{
			Key:  kind + "|" + strings.TrimRight(st.URL, "/"),
			Kind: kind,
			Name: st.Name,
			URL:  st.URL,
			Lat:  st.Latitude,
			Lon:  st.Longitude,
		})
	}
	return out, nil
}

// KiwiDirectorySource adapts the KiwiSDR public listing into sweep
// records, carrying the listing's capability hints (bands, slots,
// antenna, location, offline flag) for the probe to confirm or keep.
func KiwiDirectorySource(loader *kiwisdr.Loader) func(context.Context) ([]Record, error) {
	return func(ctx context.Context) ([]Record, error) {
		receivers, _, err := loader.Load(ctx)
		if err != nil {
			return nil, fmt.Errorf("load KiwiSDR directory: %w", err)
		}
		out := make([]Record, 0, len(receivers))
		for _, r := range receivers {
			rec := Record{
				Key:           KindKiwi + "|" + r.Key(),
				Kind:          KindKiwi,
				Name:          r.Name,
				URL:           r.URL,
				Lat:           r.Lat,
				Lon:           r.Lon,
				ListedOffline: r.Offline,
				ListedUpdated: r.Updated,
			}
			if lo, hi, ok := parseBands(r.Bands); ok {
				rec.Capability.FreqMinHz, rec.Capability.FreqMaxHz = lo, hi
			}
			rec.Capability.UsersMax = r.UsersMax
			rec.Capability.Antenna = r.Antenna
			rec.Capability.Location = r.Location
			rec.Capability.Version = r.Version
			out = append(out, rec)
		}
		return out, nil
	}
}

// parseBands reads the listing's "minHz-maxHz" coverage hint.
func parseBands(s string) (int64, int64, bool) {
	lo, hi, ok := strings.Cut(s, "-")
	if !ok {
		return 0, 0, false
	}
	a, errA := strconv.ParseInt(strings.TrimSpace(lo), 10, 64)
	b, errB := strconv.ParseInt(strings.TrimSpace(hi), 10, 64)
	if errA != nil || errB != nil || a < 0 || b <= a {
		return 0, 0, false
	}
	return a, b, true
}
