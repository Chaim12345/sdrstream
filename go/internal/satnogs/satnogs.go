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
	"net/url"
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

// Demoddata is one artefact a station produced from an observation. The
// URL usually points at a raw bitstream, a decoded image or a text file.
type Demoddata struct {
	PayloadDemod string `json:"payload_demod"`
}

// Observation is one ground station's recording of one satellite.
//
// The SatNOGS API renamed several fields over time, so UnmarshalJSON accepts
// both the current names (norad_cat_id, observation_frequency,
// transmitter_mode, waterfall, demoddata[]) and the older ones (norad_id,
// frequency, mode, waterfall_download, payload_download).
type Observation struct {
	ID           int         `json:"id"`
	Satellite    int         `json:"satellite"`
	SatName      string      `json:"satellite_name"`
	TLE0         string      `json:"tle0"`
	NoradID      int         `json:"norad_id"`
	Station      string      `json:"station_name"`
	Latitude     float64     `json:"station_lat"`
	Longitude    float64     `json:"station_lng"`
	Start        time.Time   `json:"start"`
	End          time.Time   `json:"end"`
	MaxElevation float64     `json:"max_altitude"`
	FrequencyHz  int64       `json:"observation_frequency"`
	Mode         string      `json:"transmitter_mode"`
	Status       string      `json:"status"`
	WaterfallURL string      `json:"waterfall"`
	PayloadURL   string      `json:"payload"`
	DemodURL     string      `json:"payload_download"`
	Demoddata    []Demoddata `json:"demoddata"`
}

// UnmarshalJSON normalises the current and legacy SatNOGS field names.
func (o *Observation) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID                   int         `json:"id"`
		Start                time.Time   `json:"start"`
		End                  time.Time   `json:"end"`
		Satellite            int         `json:"satellite"`
		NoradCatID           int         `json:"norad_cat_id"`
		NoradID              int         `json:"norad_id"`
		SatName              string      `json:"satellite_name"`
		TLE0                 string      `json:"tle0"`
		Station              string      `json:"station_name"`
		Latitude             float64     `json:"station_lat"`
		Longitude            float64     `json:"station_lng"`
		MaxAltitude          float64     `json:"max_altitude"`
		MaxElevation         float64     `json:"max_elevation"`
		Frequency            int64       `json:"frequency"`
		ObservationFrequency int64       `json:"observation_frequency"`
		DownlinkLow          int64       `json:"transmitter_downlink_low"`
		Mode                 string      `json:"mode"`
		TransmitterMode      string      `json:"transmitter_mode"`
		Status               string      `json:"status"`
		Waterfall            string      `json:"waterfall"`
		WaterfallDownload    string      `json:"waterfall_download"`
		Payload              string      `json:"payload"`
		PayloadDownload      string      `json:"payload_download"`
		Demoddata            []Demoddata `json:"demoddata"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	o.ID = raw.ID
	o.Start = raw.Start
	o.End = raw.End
	o.Satellite = raw.Satellite
	o.NoradID = raw.NoradCatID
	if o.NoradID == 0 {
		o.NoradID = raw.NoradID
	}
	if o.NoradID == 0 {
		o.NoradID = raw.Satellite
	}
	o.SatName = raw.SatName
	if o.SatName == "" {
		o.SatName = strings.TrimPrefix(strings.TrimSpace(raw.TLE0), "0 ")
	}
	o.TLE0 = raw.TLE0
	o.Station = raw.Station
	o.Latitude = raw.Latitude
	o.Longitude = raw.Longitude
	o.MaxElevation = raw.MaxAltitude
	if o.MaxElevation == 0 {
		o.MaxElevation = raw.MaxElevation
	}
	o.FrequencyHz = raw.ObservationFrequency
	if o.FrequencyHz == 0 {
		o.FrequencyHz = raw.DownlinkLow
	}
	if o.FrequencyHz == 0 {
		o.FrequencyHz = raw.Frequency
	}
	o.Mode = raw.TransmitterMode
	if o.Mode == "" {
		o.Mode = raw.Mode
	}
	o.Status = raw.Status
	o.WaterfallURL = raw.Waterfall
	if o.WaterfallURL == "" {
		o.WaterfallURL = raw.WaterfallDownload
	}
	o.PayloadURL = raw.Payload
	o.DemodURL = raw.PayloadDownload
	o.Demoddata = raw.Demoddata
	if o.DemodURL == "" && len(o.Demoddata) > 0 {
		o.DemodURL = o.Demoddata[0].PayloadDemod
	}
	return nil
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

// getJSON fetches and caches one API endpoint. The SatNOGS host drops
// connections and rate limits aggressively, so the request is retried with
// a backoff before giving up.
func getJSON(path string, target interface{}, timeout time.Duration) error {
	cachePath := filepath.Join(CacheDir, "satnogs_"+strings.ReplaceAll(strings.ReplaceAll(path, "/", "_"), "?", "_")+".json")
	if data, err := os.ReadFile(cachePath); err == nil {
		if err := json.Unmarshal(data, target); err == nil {
			return nil
		}
	}
	direct := networkAPI + path
	// Some networks block the SatNOGS API but not its payload store. A
	// read-only fetch proxy (for example https://api.allorigins.win/raw?url={url})
	// can be supplied through the environment; only this public feed goes
	// through it.
	targets := []string{direct}
	if proxy := os.Getenv("SDRSTREAM_SATNOGS_PROXY"); proxy != "" {
		targets = append(targets, strings.ReplaceAll(proxy, "{url}", url.QueryEscape(direct)))
	}
	var lastErr error
	for attempt := 0; attempt < 6; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 2 * time.Second)
		}
		client := &http.Client{Timeout: timeout}
		// Alternate between the direct host and the proxy: the SatNOGS
		// host drops connections under load, so a retry on the other route
		// often succeeds.
		URL := targets[attempt%len(targets)]
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
//
// The network API's "satellite" query parameter does not filter by NORAD
// catalogue id (observed 2026-10-23: satellite=25544 returned a mixed feed
// of every satellite), so the results are filtered client-side on
// norad_cat_id. Callers keep passing the NORAD id.
func Observations(noradID int, limit int, timeout time.Duration) ([]Observation, error) {
	path := fmt.Sprintf("/observations/?satellite=%d&format=json&limit=%d", noradID, limit)
	var observations []Observation
	if err := getJSON(path, &observations, timeout); err != nil {
		return nil, err
	}
	return onlyNorad(observations, noradID), nil
}

// onlyNorad keeps the observations whose NORAD id matches. An observation
// with no NORAD id is dropped when a specific satellite was requested:
// returning it would silently attribute an unknown satellite's recording
// to the target. Unlabelled rows are only kept for unfiltered queries
// (noradID <= 0), where no attribution is implied.
func onlyNorad(observations []Observation, noradID int) []Observation {
	if noradID <= 0 {
		return observations
	}
	var out []Observation
	for _, o := range observations {
		if o.NoradID == noradID {
			out = append(out, o)
		}
	}
	return out
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

// Between returns observations that started in [start, end], optionally
// restricted to one satellite. This is how the gallery reaches finished
// observations: the unfiltered feed only shows upcoming passes.
func Between(start, end time.Time, noradID, limit int, timeout time.Duration) ([]Observation, error) {
	path := fmt.Sprintf("/observations/?format=json&start=%s&end=%s&limit=%d",
		url.QueryEscape(start.UTC().Format(time.RFC3339)),
		url.QueryEscape(end.UTC().Format(time.RFC3339)), limit)
	if noradID > 0 {
		path += fmt.Sprintf("&satellite=%d", noradID)
	}
	var observations []Observation
	if err := getJSON(path, &observations, timeout); err != nil {
		return nil, err
	}
	return onlyNorad(observations, noradID), nil
}

// Transmitters returns the DB's transmitter list for one satellite.
type Transmitter struct {
	UUID        string  `json:"uuid"`
	Description string  `json:"description"`
	DownlinkLow float64 `json:"downlink_low"`
	UplinkLow   float64 `json:"uplink_low"`
	Mode        string  `json:"mode"`
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
