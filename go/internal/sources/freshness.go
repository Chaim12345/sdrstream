package sources

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// TLEEpoch extracts the epoch of the first TLE in a body of TLE text.
// Line 1 columns 19-32 hold the epoch as YYDDD.DDDDDDDD (two-digit year and
// day of year). Years 00-56 are 2000-2056, 57-99 are 1957-1999 (NORAD
// convention).
func TLEEpoch(body []byte) (time.Time, bool) {
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimRight(line, "\r")
		if len(line) < 32 || !strings.HasPrefix(line, "1 ") {
			continue
		}
		field := strings.TrimSpace(line[18:32])
		if len(field) < 5 {
			return time.Time{}, false
		}
		year, err := strconv.Atoi(field[:2])
		if err != nil {
			return time.Time{}, false
		}
		day, err := strconv.ParseFloat(field[2:], 64)
		if err != nil || day < 1 || day > 367 {
			return time.Time{}, false
		}
		fullYear := 2000 + year
		if year >= 57 {
			fullYear = 1900 + year
		}
		whole := int(day)
		fraction := day - float64(whole)
		stamp := time.Date(fullYear, 1, 1, 0, 0, 0, 0, time.UTC).
			Add(time.Duration(whole-1) * 24 * time.Hour).
			Add(time.Duration(fraction * 24 * float64(time.Hour)))
		return stamp, true
	}
	return time.Time{}, false
}

// KIndexTime extracts the newest time_tag from the NOAA SWPC planetary
// K-index product: a JSON array of objects with an ISO "time_tag" (UTC).
func KIndexTime(body []byte) (time.Time, bool) {
	var rows []struct {
		TimeTag string `json:"time_tag"`
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		return time.Time{}, false
	}
	var newest time.Time
	found := false
	for _, row := range rows {
		stamp, err := time.Parse("2006-01-02T15:04:05", strings.TrimSuffix(row.TimeTag, "Z"))
		if err != nil {
			continue
		}
		if stamp.After(newest) {
			newest = stamp
			found = true
		}
	}
	return newest, found
}
