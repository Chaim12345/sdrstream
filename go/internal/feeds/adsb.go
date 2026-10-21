package feeds

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ADSB polls the adsb.lol open API (readsb v2 format). No key is required;
// the operators ask for polite polling, so the default interval is 60s.
type ADSB struct {
	Base   string  // default https://api.adsb.lol
	Lat    float64 // receiver centre, degrees
	Lon    float64
	Radius float64 // nautical miles
	Client *http.Client
	Every  time.Duration
	Now    func() time.Time
}

func (a *ADSB) Name() string { return "adsb.lol" }
func (a *ADSB) Kind() string { return "aircraft" }

func (a *ADSB) Interval() time.Duration {
	if a.Every > 0 {
		return a.Every
	}
	return 60 * time.Second
}

func (a *ADSB) base() string {
	if a.Base != "" {
		return strings.TrimRight(a.Base, "/")
	}
	return "https://api.adsb.lol"
}

func (a *ADSB) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

type adsbReply struct {
	Aircraft []struct {
		Hex     string  `json:"hex"`
		Flight  string  `json:"flight"`
		AltBaro any     `json:"alt_baro"` // number or "ground"
		GS      float64 `json:"gs"`
		Track   float64 `json:"track"`
		Squawk  string  `json:"squawk"`
		Lat     float64 `json:"lat"`
		Lon     float64 `json:"lon"`
		SeenPos float64 `json:"seen_pos"`
		Type    string  `json:"t"`
	} `json:"ac"`
}

// Poll fetches aircraft within Radius of the configured point.
func (a *ADSB) Poll(ctx context.Context) ([]Item, error) {
	client := a.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	url := fmt.Sprintf("%s/v2/point/%.4f/%.4f/%.0f", a.base(), a.Lat, a.Lon, a.Radius)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "sdrstream feeds (personal monitor)")
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		io.Copy(io.Discard, res.Body)
		return nil, fmt.Errorf("adsb.lol: %s", res.Status)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	var reply adsbReply
	if err := json.Unmarshal(body, &reply); err != nil {
		return nil, fmt.Errorf("adsb.lol: %w", err)
	}
	when := a.now()
	items := make([]Item, 0, len(reply.Aircraft))
	for _, ac := range reply.Aircraft {
		label := strings.TrimSpace(ac.Flight)
		if label == "" {
			label = strings.ToUpper(ac.Hex)
		}
		var parts []string
		switch alt := ac.AltBaro.(type) {
		case float64:
			parts = append(parts, fmt.Sprintf("%.0f ft", alt))
		case string:
			if alt == "ground" {
				parts = append(parts, "on ground")
			}
		}
		if ac.GS > 0 {
			parts = append(parts, fmt.Sprintf("%.0f kt", ac.GS))
		}
		if ac.Type != "" {
			parts = append(parts, ac.Type)
		}
		if ac.Squawk != "" {
			parts = append(parts, "sqk "+ac.Squawk)
		}
		it := Item{
			ID:     "adsb|" + strings.ToLower(ac.Hex),
			Label:  label,
			Detail: strings.Join(parts, " · "),
			Time:   when.Add(-time.Duration(ac.SeenPos * float64(time.Second))),
			Ref:    "https://globe.adsb.lol/?icao=" + strings.ToLower(ac.Hex),
		}
		if ac.Lat != 0 || ac.Lon != 0 {
			it.Lat, it.Lon, it.HasPos = ac.Lat, ac.Lon, true
		}
		items = append(items, it)
	}
	return items, nil
}
