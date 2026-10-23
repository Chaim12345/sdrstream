package kiwisdr

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/common"
)

// ListingURL is the linkfanel mirror of the kiwisdr.com public receiver list.
const ListingURL = "http://rx.linkfanel.net/kiwisdr_com.js"

// ListingTTL is how long a fetched listing is reused. The mirror is
// regenerated about hourly, so fetching more often only loads its server.
const ListingTTL = time.Hour

// Receiver is one KiwiSDR from the public directory: the scanner's station
// type for Kiwis. Kind and ServerURL match what monitor.SourceFor expects.
type Receiver struct {
	ID       string
	Name     string
	Host     string // lowercase hostname
	Port     int    // explicit or scheme default
	Secure   bool   // listed as https
	URL      string // as listed
	Lat, Lon float64
	Users    int
	UsersMax int
	Version  string
	Bands    string // "minHz-maxHz"
	Antenna  string
	Location string
	Offline  bool
	Updated  string
}

// Key is the dedupe identity, host:port.
func (r Receiver) Key() string { return net.JoinHostPort(r.Host, strconv.Itoa(r.Port)) }

// Kind is the scanner kind string for monitor.SourceFor.
func (r Receiver) Kind() string { return "kiwi" }

// ServerURL is what kiwisdr.New accepts.
func (r Receiver) ServerURL() string { return r.URL }

// FreeSlots is the advertised free slot count; it is a hint, the server
// decides at connect time.
func (r Receiver) FreeSlots() int {
	if n := r.UsersMax - r.Users; n > 0 {
		return n
	}
	return 0
}

// Usable is false for entries listed as offline or inactive, or with no free
// slot at listing time.
func (r Receiver) Usable() bool { return !r.Offline && r.FreeSlots() > 0 }

// Client builds a connection for the receiver.
func (r Receiver) Client(opts Options) (*Client, error) { return New(r.URL, opts) }

type rawEntry map[string]any

func (e rawEntry) str(k string) string {
	switch v := e[k].(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return ""
}

var gpsPair = regexp.MustCompile(`\(\s*(-?[0-9.]+)\s*,\s*(-?[0-9.]+)\s*\)`)

// ParseListing decodes the mirror's JavaScript ("var kiwisdr_com = [...];",
// with a trailing comma inside the array) and dedupes by host:port, keeping
// the first entry unless a later duplicate is online. Entries with no
// parseable URL are skipped. The second result is the number of raw entries.
func ParseListing(page string) ([]Receiver, int, error) {
	eq := strings.Index(page, "kiwisdr_com")
	if eq < 0 {
		return nil, 0, fmt.Errorf("kiwisdr: listing has no kiwisdr_com variable")
	}
	start := strings.IndexByte(page[eq:], '[')
	end := strings.LastIndexByte(page, ']')
	if start < 0 || end < 0 || eq+start >= end {
		return nil, 0, fmt.Errorf("kiwisdr: listing has no array")
	}
	body := page[eq+start : end+1]
	body = trailingComma.ReplaceAllString(body, "]")
	var entries []rawEntry
	if err := json.Unmarshal([]byte(body), &entries); err != nil {
		return nil, 0, fmt.Errorf("kiwisdr: decode listing: %w", err)
	}
	seen := map[string]int{}
	var out []Receiver
	for _, e := range entries {
		r, ok := receiverFromEntry(e)
		if !ok {
			continue
		}
		if i, dup := seen[r.Key()]; dup {
			// Keep the first, but prefer a usable duplicate over an offline one.
			if out[i].Offline && !r.Offline {
				out[i] = r
			}
			continue
		}
		seen[r.Key()] = len(out)
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Key() < out[j].Key() })
	return out, len(entries), nil
}

var trailingComma = regexp.MustCompile(`,\s*\]\s*$`)

func receiverFromEntry(e rawEntry) (Receiver, bool) {
	raw := e.str("url")
	if raw == "" {
		return Receiver{}, false
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return Receiver{}, false
	}
	secure := u.Scheme == "https"
	port := 80
	if secure {
		port = 443
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n <= 0 || n > 65535 {
			return Receiver{}, false
		}
		port = n
	}
	r := Receiver{
		ID:       e.str("id"),
		Name:     e.str("name"),
		Host:     strings.ToLower(u.Hostname()),
		Port:     port,
		Secure:   secure,
		URL:      strings.TrimRight(raw, "/"),
		Users:    atoiOr(e.str("users"), 0),
		UsersMax: atoiOr(e.str("users_max"), 0),
		Version:  e.str("sw_version"),
		Bands:    e.str("bands"),
		Antenna:  e.str("antenna"),
		Location: e.str("loc"),
		Updated:  e.str("updated"),
		Offline:  e.str("offline") == "yes" || (e.str("status") != "" && e.str("status") != "active"),
	}
	if m := gpsPair.FindStringSubmatch(e.str("gps")); m != nil {
		r.Lat, _ = strconv.ParseFloat(m[1], 64)
		r.Lon, _ = strconv.ParseFloat(m[2], 64)
	}
	return r, true
}

// Loader fetches the listing at most once per TTL, keeping a copy on disk so
// restarts and parallel processes do not refetch either. When a refresh
// fails it serves the stale copy rather than hammering the mirror.
type Loader struct {
	URL       string
	CachePath string // empty: in-memory only
	TTL       time.Duration
	HTTP      *http.Client
	Now       func() time.Time

	mu      sync.Mutex
	page    string
	fetched time.Time
	Fetches int // network fetches made, for tests and logs

	lastAttempt time.Time
	failures    int
}

// RetryBase and RetryCap bound the failure backoff between network fetches.
// Without it a down mirror plus a polling caller becomes a retry storm.
const (
	RetryBase = time.Minute
	RetryCap  = 15 * time.Minute
)

// retryDelay returns how long to wait before another network attempt after
// n consecutive failures: 1m, 2m, 4m... capped at 15m.
func retryDelay(n int) time.Duration {
	if n <= 0 {
		return 0
	}
	d := RetryBase
	for i := 1; i < n; i++ {
		d *= 2
		if d >= RetryCap {
			return RetryCap
		}
	}
	return d
}

// NewLoader returns a Loader for the public mirror caching under cacheDir.
func NewLoader(cacheDir string) *Loader {
	l := &Loader{URL: ListingURL, TTL: ListingTTL, HTTP: &http.Client{Timeout: 30 * time.Second}, Now: time.Now}
	if cacheDir != "" {
		l.CachePath = filepath.Join(cacheDir, "kiwisdr_com.js")
	}
	return l
}

// Load returns the deduped receivers and the raw entry count.
func (l *Loader) Load(ctx context.Context) ([]Receiver, int, error) {
	page, err := l.fetch(ctx)
	if err != nil {
		return nil, 0, err
	}
	return ParseListing(page)
}

func (l *Loader) fetch(ctx context.Context) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.Now()
	ttl := l.TTL
	if ttl == 0 {
		ttl = ListingTTL
	}
	if l.page != "" && now.Sub(l.fetched) < ttl {
		return l.page, nil
	}
	if l.CachePath != "" {
		if st, err := os.Stat(l.CachePath); err == nil && now.Sub(st.ModTime()) < ttl {
			if b, err := os.ReadFile(l.CachePath); err == nil && len(b) > 0 {
				l.page, l.fetched = string(b), st.ModTime()
				return l.page, nil
			}
		}
	}
	if wait := retryDelay(l.failures) - now.Sub(l.lastAttempt); l.failures > 0 && wait > 0 {
		// A recent attempt already failed; serve stale data or the last
		// error instead of hammering the mirror.
		if l.page != "" {
			return l.page, nil
		}
		if l.CachePath != "" {
			if b, rerr := os.ReadFile(l.CachePath); rerr == nil && len(b) > 0 {
				l.page = string(b)
				l.fetched = now.Add(-ttl + time.Minute) // retry in a minute
				return l.page, nil
			}
		}
		return "", fmt.Errorf("kiwisdr: listing fetch backing off for %s after %d failures", wait.Truncate(time.Second), l.failures)
	}
	l.lastAttempt = now
	page, err := l.download(ctx)
	if err != nil {
		l.failures++
		// Serve stale data (memory first, then disk) instead of failing.
		if l.page != "" {
			return l.page, nil
		}
		if l.CachePath != "" {
			if b, rerr := os.ReadFile(l.CachePath); rerr == nil && len(b) > 0 {
				l.page = string(b)
				l.fetched = now.Add(-ttl + time.Minute) // retry in a minute
				return l.page, nil
			}
		}
		return "", err
	}
	l.failures = 0
	l.page, l.fetched = page, now
	if l.CachePath != "" {
		if err := os.MkdirAll(filepath.Dir(l.CachePath), 0o755); err == nil {
			// Best-effort cache write, but never a torn one: a shared
			// fixed .tmp could interleave two writers and persist a
			// broken page that then poisons the cache until expiry.
			// Unique temp + rename, umask-masked perms. Last writer
			// wins is fine for a cache; no lock needed.
			_ = common.WriteFileAtomic(l.CachePath, []byte(page), 0o644)
		}
	}
	return page, nil
}

func (l *Loader) download(ctx context.Context) (string, error) {
	l.Fetches++
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.URL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "sdrstream/1.0 (+https://github.com/Chaim12345/sdrstream)")
	client := l.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("kiwisdr: fetch listing: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("kiwisdr: fetch listing: HTTP %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return "", fmt.Errorf("kiwisdr: read listing: %w", err)
	}
	if !strings.Contains(string(b), "kiwisdr_com") {
		return "", fmt.Errorf("kiwisdr: listing body is not the expected script")
	}
	return string(b), nil
}
