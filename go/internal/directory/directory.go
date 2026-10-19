// Package directory sweeps the public OpenWebRX receiver directory
// (receiverbook.de/map), probes each server's /status.json for its location
// and band coverage, and reports which receivers can hear a given downlink.
//
// The directory page embeds the whole receiver list as a JavaScript array;
// labels contain brackets and quotes, so the array is extracted by bracket
// walking rather than with a JSON parser.
package directory

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Chaim12345/sdrstream/go/internal/catalogue"
)

// DirectoryURL is the public receiver map.
const DirectoryURL = "https://receiverbook.de/map"

const userAgent = "sdrstream/0.1 (+satellite)"

// Satellite bands we care about, in Hz.
var amateurBands = map[string][2]float64{
	"6m":  {50_000_000, 54_000_000},
	"2m":  {144_000_000, 148_000_000},
	"70cm": {420_000_000, 450_000_000},
	"23cm": {1240_000_000, 1300_000_000},
}

// statusResponse is the subset of /status.json the sweep uses.
type statusResponse struct {
	Receiver struct {
		Callsign string `json:"callsign"`
		Location string `json:"location"`
		GPS      struct {
			Lat float64 `json:"lat"`
			Lon float64 `json:"lon"`
		} `json:"gps"`
	} `json:"receiver"`
	SDRs []struct {
		Name       string `json:"name"`
		CenterFreq int64  `json:"center_freq"`
		SampleRate int64  `json:"sample_rate"`
		Profiles   []struct {
			Name       string `json:"name"`
			CenterFreq int64  `json:"center_freq"`
			SampleRate int64  `json:"sample_rate"`
		} `json:"profiles"`
	} `json:"sdrs"`
}

// DiscoverResult holds one probed receiver.
type DiscoverResult struct {
	URL       string
	Latitude  float64
	Longitude float64
	Bands     []catalogue.Band
	SatelliteBands []string
}

// Discover fetches the directory and probes each OpenWebRX server in
// parallel, with a per-request timeout. Returns only servers that answered.
func Discover(ctx context.Context, timeout time.Duration, limit int) ([]DiscoverResult, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	body, err := fetch(ctx, DirectoryURL, timeout)
	if err != nil {
		return nil, err
	}
	URLs := extractReceiverURLs(body)
	if limit > 0 && len(URLs) > limit {
		URLs = URLs[:limit]
	}
	results := probeAll(ctx, URLs, timeout)
	sort.Slice(results, func(i, j int) bool { return results[i].URL < results[j].URL })
	return results, nil
}

// extractReceiverURLs pulls the ws(s):// or http(s):// server URLs out of the
// directory page's embedded receiver array.
func extractReceiverURLs(page string) []string {
	pattern := regexp.MustCompile(`https?://[A-Za-z0-9._~:/?#\[\]@!$&'()*+,;=%-]+`)
	seen := map[string]bool{}
	var out []string
	for _, match := range pattern.FindAllString(page, -1) {
		URL := strings.TrimRight(strings.Split(match, "\"")[0], "/")
		if seen[URL] {
			continue
		}
		// The map page links to many documentation pages too; keep hosts that
		// look like receivers (the embedded array entries carry /rx/ or ws).
		seen[URL] = true
		out = append(out, URL)
	}
	return out
}

func probeAll(ctx context.Context, URLs []string, timeout time.Duration) []DiscoverResult {
	workers := 16
	if workers > len(URLs) {
		workers = len(URLs)
	}
	jobs := make(chan string)
	results := make(chan DiscoverResult, len(URLs))
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for URL := range jobs {
				if result, ok := probeOne(ctx, URL, timeout); ok {
					results <- result
				}
			}
		}()
	}
	for _, URL := range URLs {
		jobs <- URL
	}
	close(jobs)
	wg.Wait()
	close(results)
	out := make([]DiscoverResult, 0, len(URLs))
	for result := range results {
		out = append(out, result)
	}
	return out
}

func probeOne(ctx context.Context, baseURL string, timeout time.Duration) (DiscoverResult, bool) {
	normalized := strings.TrimRight(baseURL, "/")
	if !strings.Contains(normalized, "://") {
		normalized = "https://" + normalized
	}
	statusURL := strings.TrimRight(normalized, "/") + "/status.json"
	body, err := fetch(ctx, statusURL, timeout)
	if err != nil {
		return DiscoverResult{}, false
	}
	var status statusResponse
	if err := json.Unmarshal([]byte(body), &status); err != nil {
		return DiscoverResult{}, false
	}
	result := DiscoverResult{URL: normalized, Latitude: status.Receiver.GPS.Lat,
		Longitude: status.Receiver.GPS.Lon}
	seen := map[string]bool{}
	addBand := func(name string, center, width int64) {
		if name == "" || center <= 0 {
			return
		}
		key := fmt.Sprintf("%s@%d", name, center)
		if seen[key] {
			return
		}
		seen[key] = true
		result.Bands = append(result.Bands, catalogue.Band{
			Name: name, CenterKHz: float64(center) / 1000, SampleRateKHz: float64(width) / 1000})
	}
	for _, sdr := range status.SDRs {
		if len(sdr.Profiles) == 0 {
			addBand(sdr.Name, sdr.CenterFreq, sdr.SampleRate)
			continue
		}
		for _, profile := range sdr.Profiles {
			addBand(profile.Name, profile.CenterFreq, profile.SampleRate)
		}
	}
	for band, edges := range amateurBands {
		for _, published := range result.Bands {
			if published.LowHz() <= edges[0] && edges[1] <= published.HighHz() {
				result.SatelliteBands = append(result.SatelliteBands, band)
				break
			}
		}
	}
	sort.Strings(result.SatelliteBands)
	return result, true
}

// ForFrequency filters discovered receivers to those covering frequencyHz.
func ForFrequency(frequencyHz float64, receivers []DiscoverResult, band string) []DiscoverResult {
	var out []DiscoverResult
	for _, receiver := range receivers {
		covered := false
		for _, published := range receiver.Bands {
			if published.Covers(frequencyHz) {
				covered = true
				break
			}
		}
		if !covered {
			continue
		}
		if band != "" && !hasBand(receiver.SatelliteBands, band) {
			continue
		}
		out = append(out, receiver)
	}
	return out
}

func hasBand(bands []string, want string) bool {
	for _, band := range bands {
		if band == want {
			return true
		}
	}
	return false
}

func fetch(ctx context.Context, URL string, timeout time.Duration) (string, error) {
	request, err := http.NewRequestWithContext(ctx, "GET", URL, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("User-Agent", userAgent)
	client := &http.Client{Timeout: timeout}
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
