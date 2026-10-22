// Package sources keeps a registry of the external data sources sdrstream
// can use without KiwiSDR traffic - receiver directories, satellite data,
// space weather, weather products, spot feeds - and health-checks each one
// live from the current machine.
//
// Every check is polite: one request in flight per host, bounded global
// concurrency, a single attempt (no retry hammering), a short timeout and a
// descriptive User-Agent. Results are labelled honestly: live, stale,
// unreachable, restricted or needs-key. "Restricted" means the service
// answered but its terms do not allow automated reuse, or its real-time
// data is only available to participants; "needs-key" means the service
// works but the API requires credentials we do not have configured here.
package sources

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Status is the honest health label of one source.
type Status string

const (
	StatusLive        Status = "live"
	StatusStale       Status = "stale"
	StatusUnreachable Status = "unreachable"
	StatusRestricted  Status = "restricted"
	StatusNeedsKey    Status = "needs-key"
)

const userAgent = "sdrstream/0.1 (+sources-health)"

// Source is one registered external data source.
type Source struct {
	Name string // short identifier, e.g. "receiverbook"
	URL  string // probe target (HTTP) or informational page for TCP probes
	Kind string // grouping, e.g. "receiver-directory", "spots", "satellite-data"
	Note string // honest reuse/key note shown next to the status
	// TCPAddr, when set, is probed with a bare TCP connect instead of HTTP.
	TCPAddr string
	// FreshFor is the maximum tolerated data age when ExtractTime is set.
	FreshFor time.Duration
	// ExtractTime pulls the timestamp of the newest data out of the body.
	ExtractTime func(body []byte) (time.Time, bool)
	// Match, when set, must be found in the body for the source to be live.
	Match string
	// Forced, when non-empty, pins the status (restricted / needs-key) as
	// long as the probe itself succeeded; a failed probe still reports
	// unreachable.
	Forced Status
}

// Result is the outcome of one health check.
type Result struct {
	Source    Source        `json:"-"`
	Name      string        `json:"name"`
	Kind      string        `json:"kind"`
	URL       string        `json:"url"`
	Status    Status        `json:"status"`
	Detail    string        `json:"detail"`
	Note      string        `json:"note,omitempty"`
	Latency   time.Duration `json:"-"`
	LatencyMs int64         `json:"latency_ms"`
	Checked   time.Time     `json:"checked"`
}

// Registry is the curated list of sources. Each entry's Match/Forced/Note
// was verified against the live service.
var Registry = []Source{
	// Receiver directories (no KiwiSDR traffic needed to read them).
	{
		Name: "receiverbook", URL: "https://www.receiverbook.de/map",
		Kind: "receiver-directory", Match: `"type":"OpenWebRX"`,
		Note: "public directory of OpenWebRX, WebSDR and KiwiSDR receivers",
	},
	{
		Name: "receivermap.org", URL: "https://receivermap.org/",
		Kind: "receiver-directory",
		Note: "former OpenWebRX receiver map; domain did not resolve at last check (service appears retired, receiverbook.de is the successor)",
	},
	{
		Name: "websdr.org", URL: "http://www.websdr.org/",
		Kind: "receiver-directory", Match: "websdr.org", Forced: StatusRestricted,
		Note: "human directory reachable, but the machine-readable list (/~~websdrlistk) states it may not be re-used in an automated system without prior permission (pa3fwm@websdr.org)",
	},
	{
		Name: "rx.linkfanel.net", URL: "http://rx.linkfanel.net/",
		Kind: "receiver-directory", Match: "dyatlov",
		Note: "dyatlov receiver map; dynamic data comes from KiwiSDR.com",
	},
	{
		Name: "rx-tx.info", URL: "https://rx-tx.info/map-sdr-points",
		Kind: "receiver-directory", Match: "SDR",
		Note: "world receiver/transmitter map listing OpenWebRX, WebSDR and KiwiSDR",
	},
	// Satellite data.
	{
		Name: "satnogs-network", URL: "https://network.satnogs.org/api/observations/?format=json",
		Kind: "satellite-data", Match: "[",
		Note: "SatNOGS observation API, no key needed for reads",
	},
	{
		Name: "satnogs-db", URL: "https://db.satnogs.org/api/satellites/?format=json",
		Kind: "satellite-data", Match: "[",
		Note: "SatNOGS satellite/transmitter DB API, no key needed for reads",
	},
	{
		Name: "celestrak-tle", URL: "https://celestrak.org/NORAD/elements/gp.php?GROUP=stations&FORMAT=tle",
		Kind: "satellite-data", Match: "1 25544U", FreshFor: 10 * 24 * time.Hour,
		ExtractTime: TLEEpoch,
		Note:        "CelesTrak TLEs (stations group); staleness from TLE epoch",
	},
	{
		Name: "amsat-tle", URL: "https://www.amsat.org/tle/current/nasa.all",
		Kind: "satellite-data", Match: "1 25544U", FreshFor: 10 * 24 * time.Hour,
		ExtractTime: TLEEpoch,
		Note:        "AMSAT TLE bulletin, fallback when CelesTrak is unreachable",
	},
	// Space weather / HF propagation.
	{
		Name: "noaa-swpc-kindex", URL: "https://services.swpc.noaa.gov/products/noaa-planetary-k-index.json",
		Kind: "space-weather", Match: "time_tag", FreshFor: 6 * time.Hour,
		ExtractTime: KIndexTime,
		Note:        "NOAA SWPC planetary K-index; drives HF band-condition judgement",
	},
	// Weather products.
	{
		Name: "dwd-opendata", URL: "https://opendata.dwd.de/",
		Kind: "weather-products", Match: "weather/",
		Note: "DWD open data server (CC BY 4.0); hosts the WEFAX broadcast schedules and charts",
	},
	// Lightning.
	{
		Name: "blitzortung", URL: "https://www.blitzortung.org/en/live_lightning_maps.php",
		Kind: "lightning", Match: "ightning", Forced: StatusRestricted,
		Note: "live maps viewable in a browser; raw real-time lightning data is only distributed to participating stations",
	},
	// Spot / propagation feeds (stream transports probed separately below).
	{
		Name: "pskreporter", URL: "https://pskreporter.info/pskmap.html",
		Kind: "spots", Match: "pskreporter",
		Note: "PSK Reporter; live spot feed over MQTT (see pskreporter-mqtt)",
	},
	{
		Name: "pskreporter-mqtt", URL: "mqtt://mqtt.pskreporter.info:1883",
		Kind: "spots", TCPAddr: "mqtt.pskreporter.info:1883",
		Note: "public MQTT spot feed, no account needed",
	},
	{
		Name: "wsprnet", URL: "https://www.wsprnet.org/drupal/wsprnet/spots",
		Kind: "spots", Match: "WSPR",
		Note: "WSPR spot database (web scrape; no public API)",
	},
	{
		Name: "reversebeacon", URL: "https://www.reversebeacon.net/",
		Kind: "spots", Match: "Reverse Beacon",
		Note: "RBN; live skimmer spots over telnet (see rbn-telnet)",
	},
	{
		Name: "rbn-telnet", URL: "telnet://telnet.reversebeacon.net:7000",
		Kind: "spots", TCPAddr: "telnet.reversebeacon.net:7000",
		Note: "public DX-cluster feed, any callsign greeting works",
	},
	{
		Name: "aprs-is", URL: "aprs://rotate.aprs2.net:14580",
		Kind: "spots", TCPAddr: "rotate.aprs2.net:14580",
		Note: "APRS-IS rotating tier-2 server, read-only with any login",
	},
	{
		Name: "aprs.fi", URL: "https://aprs.fi/",
		Kind: "spots", Match: "aprs.fi", Forced: StatusNeedsKey,
		Note: "web reachable; the JSON API needs a free aprs.fi API key",
	},
	{
		Name: "aisstream", URL: "https://aisstream.io/",
		Kind: "vessels", Match: "aisstream", Forced: StatusNeedsKey,
		Note: "web reachable; the AIS websocket feed needs a free API key (already in the vault, set SDRSTREAM_AIS_KEY)",
	},
	{
		Name: "adsb.lol", URL: "https://api.adsb.lol/v2/point/51.5/-0.1/250",
		Kind: "aircraft", Match: `"ac"`,
		Note: "open ADS-B REST API, no key",
	},
}

// Options tunes the health sweep.
type Options struct {
	Timeout     time.Duration // per-request timeout (default 10s)
	Concurrency int           // global in-flight cap (default 4)
	Kinds       []string      // only these kinds (empty = all)
}

// HealthAll probes every registered source and returns one result per
// source, sorted by kind then name. It is the single entry point for the
// honest health table.
func HealthAll(ctx context.Context, opts Options) []Result {
	if opts.Timeout <= 0 {
		opts.Timeout = 10 * time.Second
	}
	if opts.Concurrency <= 0 {
		opts.Concurrency = 4
	}
	var todo []Source
	want := map[string]bool{}
	for _, kind := range opts.Kinds {
		want[strings.ToLower(kind)] = true
	}
	for _, source := range Registry {
		if len(want) > 0 && !want[strings.ToLower(source.Kind)] {
			continue
		}
		todo = append(todo, source)
	}
	results := make([]Result, len(todo))
	jobs := make(chan int)
	// Per-host politeness: a mutex per host serializes probes that share one.
	var hostLocksMu sync.Mutex
	hostLocks := map[string]*sync.Mutex{}
	hostLock := func(host string) *sync.Mutex {
		hostLocksMu.Lock()
		defer hostLocksMu.Unlock()
		lock, ok := hostLocks[host]
		if !ok {
			lock = &sync.Mutex{}
			hostLocks[host] = lock
		}
		return lock
	}
	var wg sync.WaitGroup
	for i := 0; i < opts.Concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				source := todo[index]
				lock := hostLock(hostKey(source))
				lock.Lock()
				results[index] = probe(ctx, source, opts.Timeout)
				lock.Unlock()
			}
		}()
	}
	for index := range todo {
		jobs <- index
	}
	close(jobs)
	wg.Wait()
	sort.Slice(results, func(i, j int) bool {
		if results[i].Kind != results[j].Kind {
			return results[i].Kind < results[j].Kind
		}
		return results[i].Name < results[j].Name
	})
	return results
}

func hostKey(source Source) string {
	addr := source.TCPAddr
	if addr == "" {
		addr = source.URL
	}
	addr = strings.TrimPrefix(addr, "https://")
	addr = strings.TrimPrefix(addr, "http://")
	if slash := strings.IndexByte(addr, '/'); slash >= 0 {
		addr = addr[:slash]
	}
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}

func probe(ctx context.Context, source Source, timeout time.Duration) Result {
	result := Result{
		Source: source, Name: source.Name, Kind: source.Kind,
		URL: source.URL, Note: source.Note, Checked: time.Now().UTC(),
	}
	start := time.Now()
	if source.TCPAddr != "" {
		result.Status, result.Detail = probeTCP(ctx, source.TCPAddr, timeout)
		result.Latency = time.Since(start)
		result.LatencyMs = result.Latency.Milliseconds()
		return result
	}
	body, code, err := fetch(ctx, source.URL, timeout)
	result.Latency = time.Since(start)
	result.LatencyMs = result.Latency.Milliseconds()
	if err != nil {
		result.Status = StatusUnreachable
		result.Detail = shortErr(err)
		return result
	}
	if code != http.StatusOK {
		result.Status = StatusUnreachable
		result.Detail = fmt.Sprintf("HTTP %d", code)
		return result
	}
	if source.Match != "" && !strings.Contains(string(body), source.Match) {
		result.Status = StatusUnreachable
		result.Detail = "reachable but expected content missing (service changed?)"
		return result
	}
	if source.Forced != "" {
		result.Status = source.Forced
		result.Detail = fmt.Sprintf("reachable (HTTP 200, %d bytes)", len(body))
		return result
	}
	if source.ExtractTime != nil && source.FreshFor > 0 {
		if stamp, ok := source.ExtractTime(body); ok {
			age := time.Since(stamp)
			if age > source.FreshFor {
				result.Status = StatusStale
				result.Detail = fmt.Sprintf("newest data %s (%s old, limit %s)",
					stamp.UTC().Format("2006-01-02 15:04 UTC"), age.Truncate(time.Minute), source.FreshFor)
				return result
			}
			result.Detail = fmt.Sprintf("newest data %s (%s old)",
				stamp.UTC().Format("2006-01-02 15:04 UTC"), age.Truncate(time.Minute))
		} else {
			result.Detail = fmt.Sprintf("reachable (HTTP 200, %d bytes); no data timestamp parsed", len(body))
		}
	} else {
		result.Detail = fmt.Sprintf("HTTP 200, %d bytes", len(body))
	}
	result.Status = StatusLive
	return result
}

func probeTCP(ctx context.Context, addr string, timeout time.Duration) (Status, string) {
	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return StatusUnreachable, shortErr(err)
	}
	conn.Close()
	return StatusLive, "TCP connect ok"
}

func fetch(ctx context.Context, URL string, timeout time.Duration) ([]byte, int, error) {
	request, err := http.NewRequestWithContext(ctx, "GET", URL, nil)
	if err != nil {
		return nil, 0, err
	}
	request.Header.Set("User-Agent", userAgent)
	client := &http.Client{Timeout: timeout}
	response, err := client.Do(request)
	if err != nil {
		return nil, 0, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return nil, response.StatusCode, err
	}
	return body, response.StatusCode, nil
}

func shortErr(err error) string {
	text := err.Error()
	if i := strings.LastIndex(text, ": "); i >= 0 && i+2 < len(text) {
		text = text[i+2:]
	}
	if len(text) > 120 {
		text = text[:120]
	}
	return text
}
