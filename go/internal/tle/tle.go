// Package tle fetches and caches two-line element sets from Celestrak.
//
// TLEs go stale: SGP4 error grows by roughly a kilometre a day for a low Earth
// orbit, which is tolerable for a day or two and useless after a few weeks.
// The cache here keeps the fetch time so callers can decide whether to refetch.
package tle

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Groups maps each TLE group name to its download sources, in fallback
// order. The "amateur" group holds the FM and linear satellites; "weather"
// and "noaa" hold the 137 MHz APT birds. AMSAT publishes the same amateur
// set at a different URL and is the fallback when Celestrak is unreachable
// (it rate-limits and, from some networks, times out entirely).
var Groups = map[string][]string{
	"amateur": {
		"https://celestrak.org/NORAD/elements/gp.php?GROUP=amateur&FORMAT=tle",
		"https://www.amsat.org/tle/current/nasa.all",
	},
	"weather": {
		"https://celestrak.org/NORAD/elements/gp.php?GROUP=weather&FORMAT=tle",
	},
	"stations": {
		"https://celestrak.org/NORAD/elements/gp.php?GROUP=stations&FORMAT=tle",
	},
	"cubesat": {
		"https://celestrak.org/NORAD/elements/gp.php?GROUP=cubesat&FORMAT=tle",
	},
}

// catnrURLTemplate fetches a single object by catalogue number, used for the
// few birds that no downloaded group carries.
const catnrURLTemplate = "https://celestrak.org/NORAD/elements/gp.php?CATNR=%d&FORMAT=tle"

const userAgent = "sdrstream/0.1 (+satellite)"

// DefaultCacheDir is ~/.cache/sdrstream unless SDRSTREAM_CACHE is set.
var DefaultCacheDir = func() string {
	if dir := os.Getenv("SDRSTREAM_CACHE"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".sdrstream-cache"
	}
	return filepath.Join(home, ".cache", "sdrstream")
}()

// Set holds one satellite's element set.
type Set struct {
	Name  string
	Line1 string
	Line2 string
}

// Catnr extracts the catalogue number from TLE line 1. Celestrak zero-pads it
// to five columns, so "07530" and "7530" are the same object; comparing the
// raw text would treat them as different.
func Catnr(line1 string) int {
	value := strings.TrimSpace(line1[2:7])
	number := 0
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			break
		}
		number = number*10 + int(digit-'0')
	}
	return number
}

// CachePath returns where a group's TLE set is cached.
func CachePath(group string) string {
	return filepath.Join(DefaultCacheDir, "tle_"+group+".txt")
}

// AgeSeconds reports how long ago the cached set was fetched, or -1 when absent.
func AgeSeconds(group string) float64 {
	info, err := os.Stat(CachePath(group))
	if err != nil {
		return -1
	}
	return time.Since(info.ModTime()).Seconds()
}

// looksLikeTLE reports whether a download plausibly contains element sets.
// AMSAT's nasa.all file carries a bulletin header before the TLEs, so the
// test is "contains a line starting with '1 '" rather than a magic prefix.
func looksLikeTLE(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "1 ") {
			return true
		}
	}
	return false
}

// Fetch downloads a group's TLE set and caches it, trying each source URL in
// turn until one returns something that looks like element sets.
func Fetch(group string, timeout time.Duration) (string, error) {
	urls, ok := Groups[group]
	if !ok {
		keys := make([]string, 0, len(Groups))
		for key := range Groups {
			keys = append(keys, key)
		}
		sortStrings(keys)
		return "", fmt.Errorf("unknown group %q; known: %s", group, strings.Join(keys, ", "))
	}
	var err error
	for _, URL := range urls {
		var text string
		text, err = httpGet(URL, timeout)
		if err == nil && looksLikeTLE(text) {
			if mkdirErr := os.MkdirAll(DefaultCacheDir, 0o755); mkdirErr == nil {
				_ = os.WriteFile(CachePath(group), []byte(text), 0o644)
			}
			return text, nil
		}
	}
	return "", fmt.Errorf("all %d sources for TLE group %q failed: %w", len(urls), group, err)
}

// Load returns a group's TLE set, downloading it only when the cache is
// stale. With offline set, the cached copy is used however old it is.
func Load(group string, maxAgeHours float64, offline bool, timeout time.Duration) (string, error) {
	age := AgeSeconds(group)
	if age >= 0 && (offline || age < maxAgeHours*3600) {
		data, err := os.ReadFile(CachePath(group))
		if err == nil {
			return string(data), nil
		}
	}
	if offline {
		return "", fmt.Errorf("no cached TLE set for %q; run without --offline once", group)
	}
	text, err := Fetch(group, timeout)
	if err != nil {
		if age >= 0 {
			if stale, readErr := os.ReadFile(CachePath(group)); readErr == nil {
				return string(stale), nil
			}
		}
		return "", err
	}
	return text, nil
}

// FetchCatnr downloads one object's TLE by catalogue number and caches it.
// Celestrak answers a miss with a short "No GP data found" body rather than
// an error, so the caller recognises a real TLE by its "1 " element line.
func FetchCatnr(noradID int, timeout time.Duration) (string, error) {
	URL := fmt.Sprintf(catnrURLTemplate, noradID)
	return httpGet(URL, timeout)
}

// LoadCatnr returns one object's cached TLE text, or an empty string when
// Celestrak has none. This is the fallback for table entries that no
// downloaded group carries, such as the older AO-7 and CAS-4 birds.
func LoadCatnr(noradID int, maxAgeHours float64, offline bool, timeout time.Duration) (string, error) {
	path := filepath.Join(DefaultCacheDir, fmt.Sprintf("tle_catnr_%d.txt", noradID))
	age := -1.0
	if info, err := os.Stat(path); err == nil {
		age = time.Since(info.ModTime()).Seconds()
	}
	if age >= 0 && (offline || age < maxAgeHours*3600) {
		data, _ := os.ReadFile(path)
		if strings.Contains(string(data), "1 ") {
			return string(data), nil
		}
		return "", nil
	}
	if offline {
		return "", nil
	}
	text, err := FetchCatnr(noradID, timeout)
	if err != nil {
		if data, readErr := os.ReadFile(path); readErr == nil {
			if strings.Contains(string(data), "1 ") {
				return string(data), nil
			}
		}
		return "", nil
	}
	_ = os.MkdirAll(DefaultCacheDir, 0o755)
	_ = os.WriteFile(path, []byte(text), 0o644)
	if !strings.Contains(text, "1 ") {
		return "", nil
	}
	return text, nil
}

// Parse reads a TLE file into a name-keyed map. Celestrak's format is a name
// line followed by the two element lines. Some files omit names, in which case
// the satellite number from line 1 is used.
func Parse(text string) map[string]Set {
	lines := strings.Split(text, "\n")
	out := map[string]Set{}
	for i := 0; i < len(lines); i++ {
		line := strings.TrimRight(lines[i], "\r")
		if strings.HasPrefix(line, "1 ") && i+1 < len(lines) && strings.HasPrefix(lines[i+1], "2 ") {
			name := strings.TrimSpace(line[2:7])
			out[name] = Set{Name: name, Line1: line, Line2: strings.TrimRight(lines[i+1], "\r")}
			i++
			continue
		}
		if strings.TrimSpace(line) != "" && i+2 < len(lines) && strings.HasPrefix(lines[i+1], "1 ") {
			name := strings.TrimSpace(line)
			out[name] = Set{Name: name, Line1: strings.TrimRight(lines[i+1], "\r"), Line2: strings.TrimRight(lines[i+2], "\r")}
			i += 2
			continue
		}
	}
	return out
}

// Lookup finds one satellite's TLE by name, allowing partial matches.
func Lookup(name, group string, maxAgeHours float64, offline bool, timeout time.Duration) (Set, error) {
	elements := Parse(mustLoad(group, maxAgeHours, offline, timeout))
	want := strings.ToUpper(strings.TrimSpace(name))
	if set, ok := elements[want]; ok {
		return set, nil
	}
	for key, set := range elements {
		if strings.Contains(strings.ToUpper(key), want) {
			return set, nil
		}
	}
	return Set{}, fmt.Errorf("no TLE for %q in group %q (%d satellites)", name, group, len(elements))
}

func mustLoad(group string, maxAgeHours float64, offline bool, timeout time.Duration) string {
	text, err := Load(group, maxAgeHours, offline, timeout)
	if err != nil {
		return ""
	}
	return text
}

func httpGet(URL string, timeout time.Duration) (string, error) {
	client := &http.Client{Timeout: timeout}
	request, err := http.NewRequest("GET", URL, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("User-Agent", userAgent)
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

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
