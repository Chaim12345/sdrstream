package feeds

// Parks and summits spots: POTA (Parks on the Air) and SOTA (Summits on
// the Air). Both projects publish their current spots as keyless JSON so
// that logging programs and spot maps can show activators; we poll slowly
// (minutes, not seconds) and only read.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// POTA polls api.pota.app/v1/spots, the public spots feed behind pota.app.
type POTA struct {
	Base   string // default https://api.pota.app
	Client *http.Client
	Now    func() time.Time
}

func (p *POTA) Name() string { return "pota" }
func (p *POTA) Kind() string { return "parks" }

func (p *POTA) base() string {
	if p.Base != "" {
		return strings.TrimRight(p.Base, "/")
	}
	return "https://api.pota.app"
}

func (p *POTA) Interval() time.Duration { return 90 * time.Second }

type potaSpot struct {
	SpotID    int64   `json:"spotId"`
	Activator string  `json:"activator"`
	Frequency string  `json:"frequency"` // kHz, as text
	Mode      string  `json:"mode"`
	Reference string  `json:"reference"`
	ParkName  string  `json:"name"`
	Spotter   string  `json:"spotter"`
	SpotTime  string  `json:"spotTime"`
	Comments  string  `json:"comments"`
	Lat       float64 `json:"latitude"`
	Lon       float64 `json:"longitude"`
}

func (p *POTA) Poll(ctx context.Context) ([]Item, error) {
	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.base()+"/v1/spots", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "sdrstream (https://github.com/Chaim12345/sdrstream)")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("pota: %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	var spots []potaSpot
	if err := json.Unmarshal(body, &spots); err != nil {
		return nil, err
	}
	now := time.Now()
	if p.Now != nil {
		now = p.Now()
	}
	items := make([]Item, 0, len(spots))
	for _, s := range spots {
		when := now
		if t, err := time.Parse("2006-01-02T15:04:05", s.SpotTime); err == nil {
			when = t.UTC()
		}
		freqKHz := strings.TrimSpace(s.Frequency)
		label := s.Activator
		if freqKHz != "" {
			label += " " + freqKHz + " kHz"
		}
		if s.Mode != "" {
			label += " " + s.Mode
		}
		detail := "POTA " + s.Reference
		if s.ParkName != "" {
			detail += " " + s.ParkName
		}
		if s.Spotter != "" {
			detail += " (spot by " + s.Spotter + ")"
		}
		if s.Comments != "" {
			detail += ": " + s.Comments
		}
		items = append(items, Item{
			ID:     fmt.Sprintf("pota:%d", s.SpotID),
			Source: "pota",
			Kind:   "parks",
			Time:   when,
			Label:  label,
			Detail: detail,
			Lat:    s.Lat, Lon: s.Lon, HasPos: s.Lat != 0 || s.Lon != 0,
			Ref: "https://pota.app/#/park/" + s.Reference,
		})
	}
	return items, nil
}

// SOTA polls the SOTA Watch v2 spots endpoint (api2.sota.org.uk), the
// public feed behind sotawatch.sota.org.uk.
type SOTA struct {
	Base   string // default https://api2.sota.org.uk
	Count  int    // spots per poll, default 25
	Client *http.Client
	Now    func() time.Time
}

func (s *SOTA) Name() string { return "sota" }
func (s *SOTA) Kind() string { return "parks" }

func (s *SOTA) base() string {
	if s.Base != "" {
		return strings.TrimRight(s.Base, "/")
	}
	return "https://api2.sota.org.uk"
}

func (s *SOTA) Interval() time.Duration { return 90 * time.Second }

type sotaSpot struct {
	ID        int64    `json:"id"`
	TimeStamp string   `json:"timeStamp"`
	Activator string   `json:"activatorCallsign"`
	Frequency *float64 `json:"frequency"` // MHz
	Mode      string   `json:"mode"`
	Summit    string   `json:"summitCode"`
	Name      string   `json:"summitName"`
	Comments  *string  `json:"comments"`
	Type      string   `json:"type"`
	Lat       float64  `json:"latitude"`
	Lon       float64  `json:"longitude"`
}

func (s *SOTA) Poll(ctx context.Context) ([]Item, error) {
	count := s.Count
	if count <= 0 {
		count = 25
	}
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	url := fmt.Sprintf("%s/api/spots/%d/all/all", s.base(), count)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "sdrstream (https://github.com/Chaim12345/sdrstream)")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("sota: %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	var spots []sotaSpot
	if err := json.Unmarshal(body, &spots); err != nil {
		return nil, err
	}
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	items := make([]Item, 0, len(spots))
	for _, sp := range spots {
		when := now
		if t, err := time.Parse(time.RFC3339Nano, sp.TimeStamp); err == nil {
			when = t.UTC()
		}
		label := sp.Activator
		if sp.Frequency != nil && *sp.Frequency > 0 {
			label += fmt.Sprintf(" %.3f MHz", *sp.Frequency)
		}
		if sp.Mode != "" {
			label += " " + sp.Mode
		}
		detail := "SOTA " + sp.Summit
		if sp.Name != "" {
			detail += " " + sp.Name
		}
		if sp.Type != "" {
			detail += " [" + sp.Type + "]"
		}
		if sp.Comments != nil && *sp.Comments != "" {
			detail += ": " + *sp.Comments
		}
		items = append(items, Item{
			ID:     fmt.Sprintf("sota:%d", sp.ID),
			Source: "sota",
			Kind:   "parks",
			Time:   when,
			Label:  label,
			Detail: detail,
			Lat:    sp.Lat, Lon: sp.Lon, HasPos: sp.Lat != 0 || sp.Lon != 0,
			Ref: "https://sotl.as/summits/" + sp.Summit,
		})
	}
	return items, nil
}
