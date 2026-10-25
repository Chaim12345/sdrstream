package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/common"
)

// Prober probes one station and fills the probe-outcome fields of its
// Record. Identity and listing fields are already set by the sweep.
type Prober interface {
	Probe(ctx context.Context, rec *Record)
}

// HTTPProber probes with a shared HTTP client and a per-request timeout.
// AllowPrivate must stay false for directory-sourced stations: those are
// untrusted input and must not reach internal addresses (SSRF guard via
// common.PublicHTTPClient).
type HTTPProber struct {
	HTTP    *http.Client
	Timeout time.Duration
}

// NewHTTPProber builds one with the shared public client.
func NewHTTPProber() *HTTPProber {
	return &HTTPProber{HTTP: common.PublicHTTPClient(8 * time.Second), Timeout: 8 * time.Second}
}

// Probe dispatches on the record kind.
func (p *HTTPProber) Probe(ctx context.Context, rec *Record) {
	switch rec.Kind {
	case KindOpenWebRX:
		p.probeOpenWebRX(ctx, rec)
	case KindKiwi:
		p.probeKiwi(ctx, rec)
	case KindWebSDR:
		p.probeWebSDR(ctx, rec)
	default:
		rec.Verdict = VerdictDead
		rec.LastError = "unknown kind " + strconv.Quote(rec.Kind)
	}
}

func (p *HTTPProber) get(ctx context.Context, url string) ([]byte, time.Duration, error) {
	cctx, cancel := context.WithTimeout(ctx, p.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	start := time.Now()
	resp, err := p.HTTP.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return body, time.Since(start), err
}

// owrxStatus is the capability subset of an OpenWebRX /status.json.
type owrxStatus struct {
	Receiver struct {
		Name     string `json:"name"`
		Location string `json:"location"`
		GPS      struct {
			Lat float64 `json:"lat"`
			Lon float64 `json:"lon"`
		} `json:"gps"`
		Antenna struct {
			AntennaType string `json:"antenna_type"`
		} `json:"antenna"`
	} `json:"receiver"`
	SDRs []struct {
		Name     string `json:"name"`
		Profiles []struct {
			Name string `json:"name"`
		} `json:"profiles"`
	} `json:"sdrs"`
}

func (p *HTTPProber) probeOpenWebRX(ctx context.Context, rec *Record) {
	body, latency, err := p.get(ctx, strings.TrimRight(rec.URL, "/")+"/status.json")
	rec.CheckedAt = time.Now()
	if err != nil {
		classifyErr(rec, err)
		return
	}
	var st owrxStatus
	if err := json.Unmarshal(body, &st); err != nil {
		rec.Verdict, rec.LastError = VerdictDead, "malformed status.json"
		return
	}
	rec.Reachable, rec.Verdict, rec.Latency = true, VerdictOK, latency
	if st.Receiver.Name != "" {
		rec.Name = st.Receiver.Name
	}
	if st.Receiver.GPS.Lat != 0 || st.Receiver.GPS.Lon != 0 {
		rec.Lat, rec.Lon = st.Receiver.GPS.Lat, st.Receiver.GPS.Lon
	}
	var profiles []string
	for _, sdr := range st.SDRs {
		for _, prof := range sdr.Profiles {
			profiles = append(profiles, sdr.Name+": "+prof.Name)
		}
	}
	rec.Capability.Profiles = profiles
	if st.Receiver.Antenna.AntennaType != "" {
		rec.Capability.Antenna = st.Receiver.Antenna.AntennaType
	}
}

func (p *HTTPProber) probeKiwi(ctx context.Context, rec *Record) {
	// KiwiSDR /status answers key=value lines; users/users_max are
	// mandatory on a healthy server.
	scheme := "http"
	if strings.HasPrefix(rec.URL, "https://") {
		scheme = "https"
	}
	host := strings.TrimPrefix(strings.TrimPrefix(rec.URL, "http://"), "https://")
	body, latency, err := p.get(ctx, scheme+"://"+strings.TrimRight(host, "/")+"/status")
	rec.CheckedAt = time.Now()
	if err != nil {
		classifyErr(rec, err)
		return
	}
	kv := map[string]string{}
	for _, line := range strings.Split(string(body), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			kv[k] = v
		}
	}
	users, errU := strconv.Atoi(kv["users"])
	usersMax, errM := strconv.Atoi(kv["users_max"])
	if errU != nil || errM != nil {
		rec.Verdict, rec.LastError = VerdictDead, "status lacks users/users_max"
		return
	}
	rec.Reachable = true
	rec.Latency = latency
	if users >= usersMax {
		rec.Verdict = VerdictBusy
	} else {
		rec.Verdict = VerdictOK
	}
	rec.Capability.UsersMax = usersMax
	if v := kv["version"]; v != "" {
		rec.Capability.Version = v
	}
	if v := kv["rx_chans"]; v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			rec.Capability.RXChannels = n
		}
	}
	if v := kv["passworded"]; v == "yes" || v == "1" || v == "true" {
		rec.Capability.Passworded = true
	}
	if v := kv["antenna"]; v != "" {
		rec.Capability.Antenna = v
	}
}

func (p *HTTPProber) probeWebSDR(ctx context.Context, rec *Record) {
	// WebSDR stations expose no machine-readable capability; liveness only.
	_, latency, err := p.get(ctx, rec.URL)
	rec.CheckedAt = time.Now()
	if err != nil {
		classifyErr(rec, err)
		return
	}
	rec.Reachable, rec.Verdict, rec.Latency = true, VerdictOK, latency
}

// classifyErr buckets a probe error into timeout vs dead.
func classifyErr(rec *Record, err error) {
	if strings.Contains(err.Error(), "deadline exceeded") || strings.Contains(err.Error(), "Timeout") {
		rec.Verdict, rec.LastError = VerdictTimeout, err.Error()
		return
	}
	rec.Verdict, rec.LastError = VerdictDead, err.Error()
}
