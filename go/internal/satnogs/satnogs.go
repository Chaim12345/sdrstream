// Package satnogs reads the public SatNOGS network API for real downlink
// data: what ground stations actually heard, at what frequency, with links to
// the captured audio and waterfall images.
//
// The API is unauthenticated but rate limited, so responses are cached on
// disk for the process lifetime.
package satnogs

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	networkAPI = "https://network.satnogs.org/api"
	dbAPI      = "https://db.satnogs.org/api"
	userAgent  = "sdrstream/0.1 (+satellite)"
)

// CacheDir defaults to ~/.cache/sdrstream unless SDRSTREAM_CACHE is set.
var CacheDir = func() string {
	if dir := os.Getenv("SDRSTREAM_CACHE"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".sdrstream-cache"
	}
	return filepath.Join(home, ".cache", "sdrstream")
}()

// Observation is one ground station's recording of one satellite.
type Observation struct {
	ID          int       `json:"id"`
	Satellite   int       `json:"satellite"`
	SatName     string    `json:"satellite_name"`
	NoradID     int       `json:"norad_id"`
	Station     string    `json:"station_name"`
	Latitude    float64   `json:"ground_station_latitude"`
	Longitude   float64   `json:"ground_station_longitude"`
	Start       time.Time `json:"start"`
	End         time.Time `json:"end"`
	MaxElevation float64  `json:"max_elevation"`
	FrequencyHz int64     `json:"frequency"`
	Mode        string    `json:"mode"`
	Status      string    `json:"status"`
	WaterfallURL string    `json:"waterfall_download"`
	DemodURL    string     `json:"payload_download"`
}

// Duration is the observation length; 0 when the timestamps are missing.
func (o Observation) Duration() time.Duration {
	if o.Start.IsZero() || o.End.IsZero() {
		return 0
	}
	return o.End.Sub(o.Start)
}

// Summary renders the observation the way the Python CLI does.
func (o Observation) Summary() string {
	start := "-"
	if !o.Start.IsZero() {
		start = o.Start.UTC().Format("2006-01-02 15:04")
	}
	frequency := "0"
	if o.FrequencyHz > 0 {
		frequency = fmt.Sprintf("%.4f MHz", float64(o.FrequencyHz)/1e6)
	}
	return fmt.Sprintf("#%d %s %s %s el %.0f %s %s",
		o.ID, start, o.SatName, o.Mode, o.MaxElevation, frequency, o.Status)
}

// getJSON fetches and caches one API endpoint. Retries three times because
// the network is intermittently rate limited or refuses connections.
func getJSON(path string, target interface{}, timeout time.Duration) error {
	cachePath := filepath.Join(CacheDir, "satnogs_"+strings.ReplaceAll(strings.ReplaceAll(path, "/", "_"), "?", "_")+".json")
	if data, err := os.ReadFile(cachePath); err == nil {
		if err := json.Unmarshal(data, target); err == nil {
			return nil
		}
	}
	URL := networkAPI + path
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 2 * time.Second)
		}
		client := &http.Client{Timeout: timeout}
		request, err := http.NewRequest("GET", URL, nil)
		if err != nil {
			return err
		}
		request.Header.Set("User-Agent", userAgent)
		response, err := client.Do(request)
		if err != nil {
			lastErr = err
			continue
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if response.StatusCode != 200 {
			lastErr = fmt.Errorf("%s: HTTP %d", path, response.StatusCode)
			continue
		}
		if err := json.Unmarshal(body, target); err != nil {
			return err
		}
		_ = os.MkdirAll(CacheDir, 0o755)
		_ = os.WriteFile(cachePath, body, 0o644)
		return nil
	}
	return lastErr
}

// Observations returns recent observations of one satellite by NORAD id.
func Observations(noradID int, limit int, timeout time.Duration) ([]Observation, error) {
	path := fmt.Sprintf("/observations/?satellite=%d&format=json&limit=%d", noradID, limit)
	var observations []Observation
	if err := getJSON(path, &observations, timeout); err != nil {
		return nil, err
	}
	return observations, nil
}

// Recent returns the unfiltered recent-observations feed.
func Recent(limit int, timeout time.Duration) ([]Observation, error) {
	path := fmt.Sprintf("/observations/?format=json&limit=%d", limit)
	var observations []Observation
	if err := getJSON(path, &observations, timeout); err != nil {
		return nil, err
	}
	return observations, nil
}

// Transmitters returns the DB's transmitter list for one satellite.
type Transmitter struct {
	UUID         string  `json:"uuid"`
	Description  string  `json:"description"`
	DownlinkLow  float64 `json:"downlink_low"`
	UplinkLow    float64 `json:"uplink_low"`
	Mode         string  `json:"mode"`
}

// Transmitters fetches the transmitters of one satellite from the SatNOGS DB.
// db.satnogs.org is firewalled in some sandboxes; callers should treat an
// error as "no data" rather than fatal.
func Transmitters(noradID int, timeout time.Duration) ([]Transmitter, error) {
	path := fmt.Sprintf("/transmitters/?satellite=%d&format=json", noradID)
	URL := dbAPI + path
	client := &http.Client{Timeout: timeout}
	request, err := http.NewRequest("GET", URL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", userAgent)
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != 200 {
		return nil, fmt.Errorf("transmitters: HTTP %d", response.StatusCode)
	}
	var transmitters []Transmitter
	return transmitters, json.Unmarshal(body, &transmitters)
}

// ObservedFrequencies counts the frequencies stations actually used.
func ObservedFrequencies(observations []Observation) map[int64]int {
	counts := map[int64]int{}
	for _, observation := range observations {
		if observation.FrequencyHz > 0 {
			counts[observation.FrequencyHz]++
		}
	}
	return counts
}

// Download saves a SatNOGS artefact to path.
func Download(url, path string, timeout time.Duration) error {
	client := &http.Client{Timeout: timeout}
	request, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	request.Header.Set("User-Agent", userAgent)
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return fmt.Errorf("download %s: HTTP %d", url, response.StatusCode)
	}
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = io.Copy(file, response.Body)
	return err
}
