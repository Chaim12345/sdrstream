// Package bandcond turns the NOAA SWPC planetary K-index into a rough,
// advisory-only per-band HF condition label for annotating scan targets.
// Geomagnetic activity (Kp) is one input among many - real conditions also
// depend on solar flux, time of day, path and season - so every label here
// is advisory, never a prediction.
package bandcond

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/sources"
)

// ProductURL is the NOAA SWPC planetary K-index product.
const ProductURL = "https://services.swpc.noaa.gov/products/noaa-planetary-k-index.json"

const userAgent = "sdrstream/0.1 (+bandcond)"

// hfBands are the amateur HF bands with their edges in MHz.
var hfBands = []struct {
	Name      string
	Low, High float64
}{
	{"160m", 1.8, 2.0},
	{"80m", 3.5, 4.0},
	{"60m", 5.3, 5.41},
	{"40m", 7.0, 7.3},
	{"30m", 10.1, 10.15},
	{"20m", 14.0, 14.35},
	{"17m", 18.068, 18.168},
	{"15m", 21.0, 21.45},
	{"12m", 24.89, 24.99},
	{"10m", 28.0, 29.7},
}

// BandCondition is the advisory label for one band.
type BandCondition struct {
	Band  string `json:"band"`
	Label string `json:"label"` // good, fair or poor
	Note  string `json:"note"`
}

// Conditions is the current advisory band-condition snapshot.
type Conditions struct {
	UpdatedAt time.Time       `json:"updated_at"`
	DataAt    time.Time       `json:"data_at"`
	Kp        float64         `json:"kp"`
	Geomag    string          `json:"geomag"` // quiet, unsettled, active, minor storm, storm
	Bands     []BandCondition `json:"bands"`
	Advisory  bool            `json:"advisory"` // always true
	SourceURL string          `json:"source_url"`
}

// GeomagName labels the Kp value.
func GeomagName(kp float64) string {
	switch {
	case kp <= 2:
		return "quiet"
	case kp <= 3:
		return "unsettled"
	case kp <= 4:
		return "active"
	case kp <= 5:
		return "minor storm"
	default:
		return "storm"
	}
}

// labelFor maps Kp to a band label. Low bands suffer rising noise with
// geomagnetic activity; the highest bands fade first in a storm.
func labelFor(band string, kp float64) string {
	low := band == "160m" || band == "80m" || band == "60m"
	high := band == "15m" || band == "12m" || band == "10m"
	switch {
	case kp <= 2:
		return "good"
	case kp <= 3:
		if high {
			return "fair"
		}
		return "good"
	case kp <= 4:
		if low || high {
			return "fair"
		}
		return "good"
	case kp <= 5:
		if low {
			return "poor"
		}
		return "fair"
	default:
		return "poor"
	}
}

// Classify builds the advisory snapshot from a Kp value and its timestamp.
func Classify(kp float64, dataAt time.Time) Conditions {
	geomag := GeomagName(kp)
	conditions := Conditions{
		UpdatedAt: time.Now().UTC(), DataAt: dataAt, Kp: kp,
		Geomag: geomag, Advisory: true, SourceURL: ProductURL,
	}
	for _, band := range hfBands {
		conditions.Bands = append(conditions.Bands, BandCondition{
			Band:  band.Name,
			Label: labelFor(band.Name, kp),
			Note:  fmt.Sprintf("Kp %.1f (%s) - advisory only", kp, geomag),
		})
	}
	return conditions
}

// LabelForHz returns the advisory label for the band covering hz, if any.
func (c Conditions) LabelForHz(hz float64) (string, bool) {
	band, ok := BandForHz(hz)
	if !ok {
		return "", false
	}
	for _, condition := range c.Bands {
		if condition.Band == band {
			return condition.Label, true
		}
	}
	return "", false
}

// BandForHz maps a frequency to its amateur HF band name.
func BandForHz(hz float64) (string, bool) {
	mhz := hz / 1e6
	for _, band := range hfBands {
		if mhz >= band.Low && mhz <= band.High {
			return band.Name, true
		}
	}
	return "", false
}

// Fetch downloads the current K-index from NOAA SWPC and classifies it.
// It takes the newest row's Kp and timestamp. The kp value is a JSON number
// in the "Kp" field of each object row.
func Fetch(ctx context.Context, url string, timeout time.Duration) (Conditions, error) {
	if url == "" {
		url = ProductURL
	}
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	request, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return Conditions{}, err
	}
	request.Header.Set("User-Agent", userAgent)
	client := &http.Client{Timeout: timeout}
	response, err := client.Do(request)
	if err != nil {
		return Conditions{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Conditions{}, fmt.Errorf("bandcond: HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return Conditions{}, err
	}
	kp, ok := newestKp(body)
	if !ok {
		return Conditions{}, fmt.Errorf("bandcond: no Kp rows parsed")
	}
	dataAt, _ := sources.KIndexTime(body)
	conditions := Classify(kp, dataAt)
	conditions.SourceURL = url
	return conditions, nil
}
