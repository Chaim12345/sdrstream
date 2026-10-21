package directory

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Station is one public receiver listed in the receiverbook directory.
type Station struct {
	Type      string  `json:"type"`
	Name      string  `json:"name"`
	URL       string  `json:"url"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

// directorySite mirrors one entry of the page's embedded receiver array.
type directorySite struct {
	Label    string `json:"label"`
	Location struct {
		Coordinates []float64 `json:"coordinates"` // GeoJSON order: lon, lat
	} `json:"location"`
	Receivers []struct {
		Label string `json:"label"`
		URL   string `json:"url"`
		Type  string `json:"type"`
	} `json:"receivers"`
}

// ParseStations extracts every receiver from the directory page. The page
// embeds the list as a JavaScript array literal, so the array is located by
// walking brackets (labels may contain "];") and then decoded as JSON.
func ParseStations(page string) ([]Station, error) {
	markerStart := strings.Index(page, "var receivers =")
	if markerStart < 0 {
		return nil, fmt.Errorf("directory: no receiver array")
	}
	bracket := strings.IndexByte(page[markerStart:], '[')
	if bracket < 0 {
		return nil, fmt.Errorf("directory: no receiver array")
	}
	start := markerStart + bracket

	depth := 0
	inString := false
	escaped := false
	end := -1
	for i := start; i < len(page); i++ {
		c := page[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				end = i + 1
			}
		}
		if end > 0 {
			break
		}
	}
	if end < 0 {
		return nil, fmt.Errorf("directory: unterminated receiver array")
	}
	var sites []directorySite
	if err := json.Unmarshal([]byte(page[start:end]), &sites); err != nil {
		return nil, err
	}
	var stations []Station
	for _, site := range sites {
		latitude, longitude := 0.0, 0.0
		if len(site.Location.Coordinates) >= 2 {
			longitude = site.Location.Coordinates[0]
			latitude = site.Location.Coordinates[1]
		}
		for _, receiver := range site.Receivers {
			url := strings.TrimSpace(receiver.URL)
			if url == "" {
				continue
			}
			name := receiver.Label
			if name == "" {
				name = site.Label
			}
			stations = append(stations, Station{
				Type:      receiver.Type,
				Name:      name,
				URL:       url,
				Latitude:  latitude,
				Longitude: longitude,
			})
		}
	}
	sort.SliceStable(stations, func(i, j int) bool {
		if stations[i].Type != stations[j].Type {
			return stations[i].Type < stations[j].Type
		}
		return stations[i].Name < stations[j].Name
	})
	return stations, nil
}

// FetchStations downloads and parses the directory without probing anything.
func FetchStations(ctx context.Context, timeout time.Duration) ([]Station, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	body, err := fetch(ctx, DirectoryURL, timeout)
	if err != nil {
		return nil, err
	}
	return ParseStations(body)
}

// CountByType reports how many stations of each type the directory lists.
func CountByType(stations []Station) map[string]int {
	counts := map[string]int{}
	for _, station := range stations {
		counts[station.Type]++
	}
	return counts
}
