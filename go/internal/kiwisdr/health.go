package kiwisdr

// Receiver health table. It keeps per-host probe latency, beacon SNR on a
// known station, and the last successful capture, and picks the small
// rotating pool of receivers the scanner holds instead of a fixed wide pool
// of connections. All state is in memory with JSON persistence; the table is
// safe for concurrent use.

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Health pool policy.
const (
	// HealthPoolMin and HealthPoolMax bound the rotating scan pool size.
	HealthPoolMin = 1
	HealthPoolMax = 3
	// HealthEWMAAlpha weights the newest probe latency into the running mean.
	HealthEWMAAlpha = 0.4
	// healthSNRFullScale is the beacon SNR in dB that scores 1.0.
	healthSNRFullScale = 30.0
	// healthLatencyScaleMS is the probe latency at which the latency
	// sub-score reaches 0.5.
	healthLatencyScaleMS = 2000.0
	// healthRecencyFull is how fresh a last successful capture must be for
	// the full recency boost; it decays to zero over healthRecencySpan.
	healthRecencySpan = 7 * 24 * time.Hour
	// healthCooldownBase is the cooldown after one consecutive failure; it
	// doubles per further failure, capped at healthCooldownCap.
	healthCooldownBase = 10 * time.Minute
	healthCooldownCap  = 6 * time.Hour
	// healthMaxConsecPenalty caps the exponential failure penalty.
	healthMaxConsecPenalty = 6
)

// HealthRecord is everything the table knows about one receiver host.
type HealthRecord struct {
	Host string `json:"host"`

	Probes        int       `json:"probes"`
	ProbeFailures int       `json:"probe_failures,omitempty"`
	LastProbe     time.Time `json:"last_probe,omitempty"`
	LastProbeOK   bool      `json:"last_probe_ok,omitempty"`
	// LastLatencyMS is the most recent successful probe latency; LatencyMS
	// is its EWMA.
	LastLatencyMS float64 `json:"last_latency_ms,omitempty"`
	LatencyMS     float64 `json:"latency_ms,omitempty"`

	// BeaconSNRdB is the estimated SNR of a known beacon carrier as heard
	// on this receiver (see beacon.go); zero-value means never measured.
	BeaconSNRdB      float64   `json:"beacon_snr_db,omitempty"`
	BeaconMeasuredAt time.Time `json:"beacon_measured_at,omitempty"`
	BeaconName       string    `json:"beacon_name,omitempty"`

	Captures            int       `json:"captures"`
	CaptureFailures     int       `json:"capture_failures,omitempty"`
	ConsecutiveFailures int       `json:"consecutive_failures,omitempty"`
	LastCapture         time.Time `json:"last_capture,omitempty"` // last SUCCESSFUL capture
	LastFailure         time.Time `json:"last_failure,omitempty"`

	lastPicked time.Time // rotation bookkeeping, not persisted
}

// cooldown returns how long a host stays out of rotation after
// ConsecutiveFailures capture failures.
func (r HealthRecord) cooldown() time.Duration {
	if r.ConsecutiveFailures <= 0 {
		return 0
	}
	d := healthCooldownBase
	for i := 1; i < r.ConsecutiveFailures && d < healthCooldownCap; i++ {
		d *= 2
	}
	return min(d, healthCooldownCap)
}

// inCooldown reports whether the host is currently benched after failures.
func (r HealthRecord) inCooldown(now time.Time) bool {
	if r.ConsecutiveFailures <= 0 || r.LastFailure.IsZero() {
		return false
	}
	return now.Before(r.LastFailure.Add(r.cooldown()))
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// score ranks the host 0..1. Unmeasured sub-signals stay neutral so new
// directory hosts can enter rotation; consecutive failures halve the score
// each time (floored) so flaky receivers sink.
func (r HealthRecord) score(now time.Time) float64 {
	latency := 0.5
	if r.LastProbeOK && r.LatencyMS > 0 {
		latency = 1 / (1 + r.LatencyMS/healthLatencyScaleMS)
	} else if r.Probes > 0 && !r.LastProbeOK {
		latency = 0.1
	}
	snr := 0.5
	if !r.BeaconMeasuredAt.IsZero() {
		snr = clamp01(r.BeaconSNRdB / healthSNRFullScale)
	}
	capture := 0.5
	if total := r.Captures + r.CaptureFailures; total > 0 {
		capture = float64(r.Captures) / float64(total)
	}
	if !r.LastCapture.IsZero() {
		age := now.Sub(r.LastCapture)
		boost := clamp01(1 - age.Hours()/healthRecencySpan.Hours())
		capture = 0.7*capture + 0.3*boost
	}
	score := 0.30*latency + 0.35*snr + 0.35*capture
	if r.ConsecutiveFailures > 0 {
		score *= math.Pow(0.5, float64(min(r.ConsecutiveFailures, healthMaxConsecPenalty)))
	}
	return score
}

// HealthTable tracks receiver health and picks the rotating scan pool.
type HealthTable struct {
	Now func() time.Time

	mu      sync.Mutex
	records map[string]*HealthRecord
}

// NewHealthTable returns an empty table.
func NewHealthTable() *HealthTable {
	return &HealthTable{Now: time.Now, records: map[string]*HealthRecord{}}
}

func (t *HealthTable) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

func (t *HealthTable) record(host string) *HealthRecord {
	host = hostOnly(host)
	r, ok := t.records[host]
	if !ok {
		r = &HealthRecord{Host: host}
		t.records[host] = r
	}
	return r
}

// ObserveProbe records one preflight result. latency is only meaningful when
// ok is true; a failed probe carries no latency signal.
func (t *HealthTable) ObserveProbe(host string, latency time.Duration, ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	r := t.record(host)
	r.Probes++
	r.LastProbe = t.now()
	r.LastProbeOK = ok
	if ok {
		ms := float64(latency) / float64(time.Millisecond)
		r.LastLatencyMS = ms
		if r.LatencyMS == 0 {
			r.LatencyMS = ms
		} else {
			r.LatencyMS = (1-HealthEWMAAlpha)*r.LatencyMS + HealthEWMAAlpha*ms
		}
	} else {
		r.ProbeFailures++
	}
}

// ObserveBeaconSNR records a beacon SNR measurement in dB.
func (t *HealthTable) ObserveBeaconSNR(host, beaconName string, snrDB float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	r := t.record(host)
	r.BeaconSNRdB = snrDB
	r.BeaconName = beaconName
	r.BeaconMeasuredAt = t.now()
}

// ObserveCapture records the outcome of one scan/capture visit. A failed
// visit starts or grows the consecutive-failure streak and benches the host
// for an exponential cooldown; a success clears it.
func (t *HealthTable) ObserveCapture(host string, ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	r := t.record(host)
	if ok {
		r.Captures++
		r.ConsecutiveFailures = 0
		r.LastCapture = t.now()
		return
	}
	r.CaptureFailures++
	r.ConsecutiveFailures++
	r.LastFailure = t.now()
}

// Score exposes the record's current score for diagnostics and tests.
func (t *HealthTable) Score(host string) float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	if r, ok := t.records[hostOnly(host)]; ok {
		return r.score(t.now())
	}
	return HealthRecord{Host: hostOnly(host)}.score(t.now())
}

// Snapshot returns a copy of all records, sorted by score (best first).
func (t *HealthTable) Snapshot() []HealthRecord {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	out := make([]HealthRecord, 0, len(t.records))
	for _, r := range t.records {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		si, sj := out[i].score(now), out[j].score(now)
		if si == sj {
			return out[i].Host < out[j].Host
		}
		return si > sj
	})
	return out
}

// Pick chooses up to n hosts (clamped to 1..3) from candidates, by health
// score. Rotation: from the 2n best-scored eligible candidates the n least
// recently picked are returned, so the pool cycles through healthy receivers
// instead of pinning the same ones. Hosts in failure cooldown are skipped.
// Candidates with no data yet score neutral and keep the pool filled while
// the table is still learning.
func (t *HealthTable) Pick(candidates []string, n int) []string {
	n = max(HealthPoolMin, min(HealthPoolMax, n))
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	type cand struct {
		host       string
		score      float64
		lastPicked time.Time
	}
	seen := map[string]bool{}
	var eligible []cand
	for _, h := range candidates {
		host := hostOnly(h)
		if host == "" || seen[host] {
			continue
		}
		seen[host] = true
		r, ok := t.records[host]
		if !ok {
			eligible = append(eligible, cand{host: host, score: HealthRecord{Host: host}.score(now)})
			continue
		}
		if r.inCooldown(now) {
			continue
		}
		eligible = append(eligible, cand{host: host, score: r.score(now), lastPicked: r.lastPicked})
	}
	if len(eligible) == 0 {
		return nil
	}
	sort.SliceStable(eligible, func(i, j int) bool {
		if eligible[i].score == eligible[j].score {
			return eligible[i].host < eligible[j].host
		}
		return eligible[i].score > eligible[j].score
	})
	// Rotation window: the 2n best candidates compete on least-recently-picked.
	window := min(len(eligible), 2*n)
	pool := eligible[:window]
	sort.SliceStable(pool, func(i, j int) bool {
		if pool[i].lastPicked.Equal(pool[j].lastPicked) {
			return pool[i].score > pool[j].score
		}
		return pool[i].lastPicked.Before(pool[j].lastPicked)
	})
	picked := make([]string, 0, n)
	for _, c := range pool[:min(n, len(pool))] {
		picked = append(picked, c.host)
		if r, ok := t.records[c.host]; ok {
			r.lastPicked = now
		} else {
			t.record(c.host).lastPicked = now
		}
	}
	return picked
}

// healthFile is the persisted form: records only, no rotation bookkeeping.
type healthFile struct {
	Version int            `json:"version"`
	SavedAt time.Time      `json:"saved_at"`
	Records []HealthRecord `json:"records"`
}

// SaveHealth writes the table to path atomically (0600, temp + rename).
func (t *HealthTable) SaveHealth(path string) error {
	t.mu.Lock()
	f := healthFile{Version: 1, SavedAt: t.now(), Records: make([]HealthRecord, 0, len(t.records))}
	for _, r := range t.records {
		cp := *r
		cp.lastPicked = time.Time{}
		f.Records = append(f.Records, cp)
	}
	t.mu.Unlock()
	sort.Slice(f.Records, func(i, j int) bool { return f.Records[i].Host < f.Records[j].Host })
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".health-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err = tmp.Chmod(0600); err == nil {
		_, err = tmp.Write(append(b, '\n'))
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// LoadHealth reads a table written by SaveHealth. A missing file returns an
// empty table, not an error: first run has no history.
func LoadHealth(path string) (*HealthTable, error) {
	t := NewHealthTable()
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return t, nil
		}
		return nil, err
	}
	var f healthFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	for i := range f.Records {
		r := f.Records[i]
		if r.Host == "" {
			continue
		}
		rc := r
		t.records[hostOnly(r.Host)] = &rc
	}
	return t, nil
}
