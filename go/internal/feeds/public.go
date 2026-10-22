package feeds

// Keyless public feeds. Provider JSON is normalized to display-only Items;
// identifiers are namespaced and each timestamp is the upstream observation time.
import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"time"
)

type PublicJSON struct {
	Source   string
	Endpoint string // optional override for tests/private mirrors
	Client   *http.Client
}

func (p *PublicJSON) Name() string { return p.Source }
func (p *PublicJSON) Kind() string {
	switch p.Source {
	case "noaa-swpc":
		return "space-weather"
	case "wspr.live":
		return "digimode"
	case "sondehub":
		return "radiosonde"
	case "digitraffic":
		return "ais"
	}
	return "data"
}
func (p *PublicJSON) Interval() time.Duration {
	if p.Source == "noaa-swpc" || p.Source == "sondehub" {
		return 5 * time.Minute
	}
	return 2 * time.Minute
}
func (p *PublicJSON) endpoint() string {
	if p.Endpoint != "" {
		return p.Endpoint
	}
	switch p.Source {
	case "noaa-swpc":
		return "https://services.swpc.noaa.gov/products/noaa-planetary-k-index.json"
	case "wspr.live":
		return "https://db1.wspr.live/?" + url.Values{"query": {"SELECT time, tx_sign, tx_loc, rx_sign, frequency, snr FROM wspr.rx WHERE time > now() - INTERVAL 15 MINUTE ORDER BY time DESC LIMIT 100 FORMAT JSON"}}.Encode()
	case "sondehub":
		return "https://api.v2.sondehub.org/sondes/telemetry?duration=1h"
	case "digitraffic":
		return "https://meri.digitraffic.fi/api/ais/v1/locations"
	}
	return ""
}
func (p *PublicJSON) Poll(ctx context.Context) ([]Item, error) {
	if p.endpoint() == "" {
		return nil, fmt.Errorf("unknown public feed %q", p.Source)
	}
	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.endpoint(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "sdrstream/1.0 (public data monitor; cached)")
	req.Header.Set("Accept", "application/json")
	// Digitraffic requires gzip. SondeHub sometimes serves gzip without a
	// Content-Encoding header, so sniff the bounded stream rather than guessing.
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("Digitraffic-User", "sdrstream/public-panels")
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", p.Source, res.StatusCode)
	}
	r := bufio.NewReader(io.LimitReader(res.Body, 12<<20))
	var body io.Reader = r
	if magic, _ := r.Peek(2); len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		z, e := gzip.NewReader(r)
		if e != nil {
			return nil, e
		}
		defer z.Close()
		body = z
	}
	b, err := io.ReadAll(io.LimitReader(body, (12<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(b) > 12<<20 {
		return nil, fmt.Errorf("%s: response too large", p.Source)
	}
	items, err := p.parse(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p.Source, err)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Time.After(items[j].Time) })
	if len(items) > 150 {
		items = items[:150]
	}
	return items, nil
}
func feedTime(s string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02 15:04:05"} {
		if t, e := time.Parse(layout, s); e == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid observation time %q", s)
}
func validPos(lat, lon float64) bool { return lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180 }
func (p *PublicJSON) parse(b []byte) ([]Item, error) {
	var out []Item
	switch p.Source {
	case "noaa-swpc":
		var rows []struct {
			Time     string  `json:"time_tag"`
			Kp       float64 `json:"Kp"`
			A        int     `json:"a_running"`
			Stations int     `json:"station_count"`
		}
		if err := json.Unmarshal(b, &rows); err != nil {
			return nil, err
		}
		for _, r := range rows {
			t, e := feedTime(r.Time)
			if e != nil {
				continue
			}
			out = append(out, Item{ID: "swpc|kp", Time: t, Label: fmt.Sprintf("Planetary Kp %.2g", r.Kp), Detail: fmt.Sprintf("3-hour observed Kp · running ap %d · %d stations · not a local reception forecast", r.A, r.Stations), Ref: p.endpoint()})
		}
		// Only the most recent Kp observation, not seven days of history.
		sort.Slice(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
		if len(out) > 1 {
			out = out[:1]
		}
	case "wspr.live":
		var reply struct {
			Data []struct {
				Time string  `json:"time"`
				Tx   string  `json:"tx_sign"`
				Grid string  `json:"tx_loc"`
				Rx   string  `json:"rx_sign"`
				Freq float64 `json:"frequency"`
				SNR  float64 `json:"snr"`
			} `json:"data"`
		}
		if err := json.Unmarshal(b, &reply); err != nil {
			return nil, err
		}
		if reply.Data == nil {
			return nil, fmt.Errorf("missing data array")
		}
		for _, r := range reply.Data {
			t, e := feedTime(r.Time)
			if e != nil || r.Tx == "" {
				continue
			}
			it := Item{ID: fmt.Sprintf("wspr.live|%s|%s|%s|%.0f", r.Time, r.Tx, r.Rx, r.Freq), Time: t, Label: r.Tx, Detail: fmt.Sprintf("WSPR %.6f MHz · %.0f dB · heard by %s · grid %s", r.Freq/1e6, r.SNR, r.Rx, r.Grid), Ref: "https://wspr.live/"}
			it.Lat, it.Lon, it.HasPos = maidenheadToLatLon(r.Grid)
			out = append(out, it)
		}
	case "sondehub":
		type telemetry struct {
			Serial string   `json:"serial"`
			Time   string   `json:"datetime"`
			Type   string   `json:"type"`
			Lat    *float64 `json:"lat"`
			Lon    *float64 `json:"lon"`
			Alt    float64  `json:"alt"`
			Freq   float64  `json:"frequency"`
		}
		var reply map[string]map[string]telemetry
		if err := json.Unmarshal(b, &reply); err != nil {
			return nil, err
		}
		for serial, history := range reply {
			var latest Item
			for _, r := range history {
				t, e := feedTime(r.Time)
				if e != nil || !t.After(latest.Time) || r.Lat == nil || r.Lon == nil || !validPos(*r.Lat, *r.Lon) {
					continue
				}
				latest = Item{ID: "sondehub|" + serial, Label: serial, Time: t, Lat: *r.Lat, Lon: *r.Lon, HasPos: true, Detail: fmt.Sprintf("%s · %.0f m altitude · %.3f MHz", r.Type, r.Alt, r.Freq), Ref: "https://sondehub.org/"}
			}
			if latest.ID != "" {
				out = append(out, latest)
			}
		}
	case "digitraffic":
		var reply struct {
			Type     string `json:"type"`
			Features []struct {
				MMSI     int64 `json:"mmsi"`
				Geometry struct {
					Coordinates []float64 `json:"coordinates"`
				} `json:"geometry"`
				Properties struct {
					MMSI int64   `json:"mmsi"`
					SOG  float64 `json:"sog"`
					COG  float64 `json:"cog"`
					Time int64   `json:"timestampExternal"`
				} `json:"properties"`
			} `json:"features"`
		}
		if err := json.Unmarshal(b, &reply); err != nil {
			return nil, err
		}
		if reply.Type != "FeatureCollection" {
			return nil, fmt.Errorf("missing FeatureCollection")
		}
		for _, r := range reply.Features {
			c := r.Geometry.Coordinates
			if len(c) < 2 || !validPos(c[1], c[0]) || r.Properties.Time <= 0 {
				continue
			}
			mmsi := r.MMSI
			if mmsi == 0 {
				mmsi = r.Properties.MMSI
			}
			if mmsi == 0 {
				continue
			}
			out = append(out, Item{ID: fmt.Sprintf("digitraffic|%d", mmsi), Label: fmt.Sprintf("MMSI %d", mmsi), Time: time.UnixMilli(r.Properties.Time).UTC(), Lat: c[1], Lon: c[0], HasPos: true, Detail: fmt.Sprintf("Baltic AIS · %.1f kt · course %.0f°", r.Properties.SOG, r.Properties.COG), Ref: "https://www.digitraffic.fi/en/marine-traffic/"})
		}
	default:
		return nil, fmt.Errorf("unknown feed")
	}
	return out, nil
}
