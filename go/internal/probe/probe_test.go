package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestCatalogSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "probe-catalog.json")
	c := NewCatalog()
	c.Update(Record{Key: "host:8073", Kind: KindKiwi, Name: "test", Reachable: true, Verdict: VerdictOK, CheckedAt: time.Now()})
	if err := c.Save(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("catalog must be 0600, got %o", info.Mode().Perm())
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	rec := loaded.Records["host:8073"]
	if rec == nil || rec.Name != "test" || rec.Verdict != VerdictOK {
		t.Fatalf("round trip lost the record: %+v", rec)
	}
}

func TestLoadMissingIsEmpty(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil || len(c.Records) != 0 {
		t.Fatalf("missing file must be an empty catalog, got %v %v", c, err)
	}
}

func TestLoadVersionMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.json")
	if err := os.WriteFile(path, []byte(`{"version":99,"records":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("version mismatch must error, not silently load")
	}
}

func TestUpdateKeepsCapabilityAcrossFailures(t *testing.T) {
	c := NewCatalog()
	c.Update(Record{Key: "h:8073", Kind: KindKiwi, Reachable: true, Verdict: VerdictOK,
		Capability: Capability{UsersMax: 4, Antenna: "vertical"}})
	c.Update(Record{Key: "h:8073", Kind: KindKiwi, Reachable: false, Verdict: VerdictTimeout,
		LastError: "timeout"})
	rec := c.Records["h:8073"]
	if rec.Reachable {
		t.Fatal("station must be down")
	}
	if rec.Capability.UsersMax != 4 || rec.Capability.Antenna != "vertical" {
		t.Fatalf("failed probe erased known capability: %+v", rec.Capability)
	}
	if rec.ConsecFailures != 1 {
		t.Fatalf("ConsecFailures = %d, want 1", rec.ConsecFailures)
	}
	c.Update(Record{Key: "h:8073", Kind: KindKiwi, Reachable: true, Verdict: VerdictOK,
		Capability: Capability{UsersMax: 8}})
	rec = c.Records["h:8073"]
	if rec.ConsecFailures != 0 || rec.Capability.UsersMax != 8 {
		t.Fatalf("recovery must reset failures and refresh capability: %+v", rec)
	}
}

func TestProbeOpenWebRX(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status.json" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		fmt.Fprint(w, `{"receiver":{"name":"TestRX","gps":{"lat":52.1,"lon":5.1},"antenna":{"antenna_type":"MLA-30"}},"sdrs":[{"name":"rtlsdr","profiles":[{"name":"70cm"},{"name":"2m"}]}]}`)
	}))
	defer srv.Close()
	p := &HTTPProber{HTTP: srv.Client(), Timeout: 2 * time.Second}
	rec := &Record{Key: "x", Kind: KindOpenWebRX, URL: srv.URL}
	p.Probe(context.Background(), rec)
	if !rec.Reachable || rec.Verdict != VerdictOK {
		t.Fatalf("probe: %+v", rec)
	}
	if rec.Name != "TestRX" || rec.Lat != 52.1 {
		t.Fatalf("identity/gps not taken from status: %+v", rec)
	}
	if len(rec.Capability.Profiles) != 2 || rec.Capability.Antenna != "MLA-30" {
		t.Fatalf("capability: %+v", rec.Capability)
	}
}

func TestProbeKiwi(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		fmt.Fprint(w, "users=4\nusers_max=4\nversion=1.800\nrx_chans=14\nantenna=Mini-Whip\n")
	}))
	defer srv.Close()
	p := &HTTPProber{HTTP: srv.Client(), Timeout: 2 * time.Second}
	rec := &Record{Key: "x", Kind: KindKiwi, URL: srv.URL}
	p.Probe(context.Background(), rec)
	if !rec.Reachable || rec.Verdict != VerdictBusy {
		t.Fatalf("full station must be busy: %+v", rec)
	}
	if rec.Capability.UsersMax != 4 || rec.Capability.Version != "1.800" || rec.Capability.RXChannels != 14 || rec.Capability.Antenna != "Mini-Whip" {
		t.Fatalf("capability: %+v", rec.Capability)
	}
}

func TestProbeKiwiMalformed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "<html>not a kiwi</html>")
	}))
	defer srv.Close()
	p := &HTTPProber{HTTP: srv.Client(), Timeout: 2 * time.Second}
	rec := &Record{Key: "x", Kind: KindKiwi, URL: srv.URL}
	p.Probe(context.Background(), rec)
	if rec.Reachable || rec.Verdict != VerdictDead {
		t.Fatalf("malformed status must be dead: %+v", rec)
	}
}

func TestProbeWebSDRLivenessOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "<html>websdr</html>")
	}))
	defer srv.Close()
	p := &HTTPProber{HTTP: srv.Client(), Timeout: 2 * time.Second}
	rec := &Record{Key: "x", Kind: KindWebSDR, URL: srv.URL}
	p.Probe(context.Background(), rec)
	if !rec.Reachable || rec.Verdict != VerdictOK {
		t.Fatalf("websdr liveness: %+v", rec)
	}
}

func TestSweepMergesSourcesAndDedupes(t *testing.T) {
	probed := map[string]int{}
	var probedMu sync.Mutex
	s := &Scheduler{
		Source: Source{
			OpenDirectory: func(ctx context.Context) ([]Record, error) {
				return []Record{{Key: "a", Kind: KindOpenWebRX}, {Key: "b", Kind: KindWebSDR}}, nil
			},
			KiwiDirectory: func(ctx context.Context) ([]Record, error) {
				return []Record{{Key: "b", Kind: KindKiwi}, {Key: "c", Kind: KindKiwi}}, nil
			},
			Manual: []Record{{Key: "m", Kind: KindKiwi}},
		},
		Prober: proberFunc(func(ctx context.Context, rec *Record) {
			probedMu.Lock()
			probed[rec.Key]++
			probedMu.Unlock()
			rec.Reachable, rec.Verdict, rec.CheckedAt = true, VerdictOK, time.Now()
		}),
		Workers: 4,
	}
	c := NewCatalog()
	if err := s.Sweep(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if len(c.Records) != 4 {
		t.Fatalf("want 4 deduped records (b from manual/open wins once), got %d", len(c.Records))
	}
	probedMu.Lock()
	for k, n := range probed {
		if n != 1 {
			t.Fatalf("station %s probed %d times", k, n)
		}
	}
	probedMu.Unlock()
}

// TestConcurrentReadDuringSweep pins the catalog's race safety: readers
// (Snapshot, Reachable) must run cleanly while a sweep writes. Fails under
// -race without the catalog lock.
func TestConcurrentReadDuringSweep(t *testing.T) {
	c := NewCatalog()
	stop := make(chan struct{})
	s := &Scheduler{
		Source: Source{Manual: []Record{{Key: "a", Kind: KindKiwi}, {Key: "b", Kind: KindKiwi}}},
		Prober: proberFunc(func(ctx context.Context, rec *Record) {
			rec.Reachable, rec.Verdict, rec.CheckedAt = true, VerdictOK, time.Now()
		}),
	}
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				_ = s.Sweep(context.Background(), c)
			}
		}
	}()
	for i := 0; i < 200; i++ {
		snap := c.Snapshot()
		_ = len(snap.Records)
		_ = c.Reachable()
	}
	close(stop)
}

// TestReachableSortedOldestFirst pins the documented order.
func TestReachableSortedOldestFirst(t *testing.T) {
	c := NewCatalog()
	now := time.Now()
	c.Update(Record{Key: "new", Kind: KindKiwi, Reachable: true, CheckedAt: now})
	c.Update(Record{Key: "old", Kind: KindKiwi, Reachable: true, CheckedAt: now.Add(-time.Hour)})
	c.Update(Record{Key: "down", Kind: KindKiwi, Reachable: false, CheckedAt: now.Add(-2 * time.Hour)})
	out := c.Reachable()
	if len(out) != 2 || out[0].Key != "old" || out[1].Key != "new" {
		t.Fatalf("Reachable order: %v", out)
	}
}

func TestSweepDirectoryFailureKeepsCatalog(t *testing.T) {
	s := &Scheduler{
		Source: Source{
			OpenDirectory: func(ctx context.Context) ([]Record, error) {
				return nil, fmt.Errorf("directory down")
			},
		},
		Prober: proberFunc(func(ctx context.Context, rec *Record) {}),
	}
	c := NewCatalog()
	c.Update(Record{Key: "old", Kind: KindKiwi, Reachable: true})
	err := s.Sweep(context.Background(), c)
	if err == nil {
		t.Fatal("directory failure must surface")
	}
	if c.Records["old"] == nil {
		t.Fatal("directory failure blanked the catalog")
	}
}

func TestCatalogJSONShape(t *testing.T) {
	c := NewCatalog()
	c.Update(Record{Key: "h:8073", Kind: KindKiwi, Reachable: true, Verdict: VerdictOK})
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var back Catalog
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if back.Records["h:8073"].Verdict != VerdictOK {
		t.Fatalf("json shape changed: %s", data)
	}
}

type proberFunc func(ctx context.Context, rec *Record)

func (f proberFunc) Probe(ctx context.Context, rec *Record) { f(ctx, rec) }

func TestBackoffDoublesAndCaps(t *testing.T) {
	base := time.Hour
	cases := []struct {
		failures int
		want     time.Duration
	}{
		{0, base}, {1, base}, {2, 2 * base}, {3, 4 * base}, {4, 8 * base},
		{5, 16 * base}, {6, 24 * time.Hour}, {10, 24 * time.Hour},
	}
	for _, tc := range cases {
		if got := Backoff(base, tc.failures); got != tc.want {
			t.Fatalf("Backoff(1h, %d) = %v, want %v", tc.failures, got, tc.want)
		}
	}
}

func TestSweepSkipsStationsNotYetDue(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	c := NewCatalog()
	// Station "dead" was checked an hour ago and is not due for another hour;
	// station "fresh" has never been seen (zero NextCheckAt = due now).
	c.Update(Record{Key: "dead", Kind: KindKiwi, Reachable: false, Verdict: VerdictTimeout,
		CheckedAt: now.Add(-time.Hour), NextCheckAt: now.Add(time.Hour)})
	var probedMu sync.Mutex
	probed := map[string]int{}
	s := &Scheduler{
		Source: Source{Manual: []Record{{Key: "dead", Kind: KindKiwi}, {Key: "fresh", Kind: KindKiwi}}},
		Prober: proberFunc(func(ctx context.Context, rec *Record) {
			probedMu.Lock()
			probed[rec.Key]++
			probedMu.Unlock()
			rec.Reachable, rec.Verdict, rec.CheckedAt = true, VerdictOK, now
		}),
		Now: func() time.Time { return now },
	}
	if err := s.Sweep(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	probedMu.Lock()
	defer probedMu.Unlock()
	if probed["dead"] != 0 {
		t.Fatalf("not-due station was probed %d times", probed["dead"])
	}
	if probed["fresh"] != 1 {
		t.Fatalf("fresh station probed %d times, want 1", probed["fresh"])
	}
	// The skipped station's catalog record must be untouched.
	if got := c.Records["dead"].CheckedAt; !got.Equal(now.Add(-time.Hour)) {
		t.Fatalf("skipped station record mutated: CheckedAt %v", got)
	}
}

func TestSweepSetsNextCheckBackoff(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	c := NewCatalog()
	// "flaky" already failed twice in a row; "good" is healthy.
	c.Update(Record{Key: "flaky", Kind: KindKiwi, Reachable: false, ConsecFailures: 2, CheckedAt: now.Add(-4 * time.Hour)})
	s := &Scheduler{
		Source: Source{Manual: []Record{{Key: "flaky", Kind: KindKiwi}, {Key: "good", Kind: KindKiwi}}},
		Prober: proberFunc(func(ctx context.Context, rec *Record) {
			rec.CheckedAt = now
			if rec.Key == "good" {
				rec.Reachable, rec.Verdict = true, VerdictOK
			} else {
				rec.Reachable, rec.Verdict = false, VerdictTimeout
			}
		}),
		Interval: time.Hour,
		Now:      func() time.Time { return now },
	}
	if err := s.Sweep(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	// Third consecutive failure: backoff 4h (2^(3-1) * base).
	if got, want := c.Records["flaky"].NextCheckAt, now.Add(4*time.Hour); !got.Equal(want) {
		t.Fatalf("flaky NextCheckAt = %v, want %v", got, want)
	}
	// Healthy station: plain interval.
	if got, want := c.Records["good"].NextCheckAt, now.Add(time.Hour); !got.Equal(want) {
		t.Fatalf("good NextCheckAt = %v, want %v", got, want)
	}
}

func TestUpdateRecordsHealthTransitions(t *testing.T) {
	c := NewCatalog()
	at := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	// New station appearing is not a transition.
	c.Update(Record{Key: "a", Kind: KindKiwi, Reachable: true, CheckedAt: at})
	if n := len(c.Events); n != 0 {
		t.Fatalf("new station produced %d events", n)
	}
	// reachable -> unreachable: one "unreachable" event with the verdict.
	c.Update(Record{Key: "a", Kind: KindKiwi, Reachable: false, Verdict: VerdictTimeout, CheckedAt: at.Add(time.Hour)})
	if len(c.Events) != 1 || c.Events[0].Kind != EventUnreachable || c.Events[0].Verdict != VerdictTimeout {
		t.Fatalf("unreachable transition events: %+v", c.Events)
	}
	// Steady state: no further events.
	c.Update(Record{Key: "a", Kind: KindKiwi, Reachable: false, Verdict: VerdictTimeout, CheckedAt: at.Add(2 * time.Hour)})
	if len(c.Events) != 1 {
		t.Fatalf("steady dead produced extra events: %+v", c.Events)
	}
	// unreachable -> reachable: one "recovered" event.
	c.Update(Record{Key: "a", Kind: KindKiwi, Reachable: true, Verdict: VerdictOK, CheckedAt: at.Add(3 * time.Hour)})
	if len(c.Events) != 2 || c.Events[1].Kind != EventRecovered {
		t.Fatalf("recovery transition events: %+v", c.Events)
	}
	if !c.Events[1].At.Equal(at.Add(3 * time.Hour)) {
		t.Fatalf("event time = %v", c.Events[1].At)
	}
}

func TestEventsRingIsBounded(t *testing.T) {
	c := NewCatalog()
	at := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	up := true
	for i := 0; i < 300; i++ {
		up = !up
		c.Update(Record{Key: "a", Kind: KindKiwi, Reachable: up, CheckedAt: at.Add(time.Duration(i) * time.Minute)})
	}
	if len(c.Events) != maxEvents {
		t.Fatalf("events = %d, want ring cap %d", len(c.Events), maxEvents)
	}
	// Newest event is the most recent transition.
	last := c.Events[len(c.Events)-1]
	if !last.At.Equal(at.Add(299 * time.Minute)) {
		t.Fatalf("newest event at %v", last.At)
	}
}

func TestEventsSurviveSnapshotAndSaveLoad(t *testing.T) {
	dir := t.TempDir()
	c := NewCatalog()
	at := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	c.Update(Record{Key: "a", Kind: KindKiwi, Reachable: true, CheckedAt: at})
	c.Update(Record{Key: "a", Kind: KindKiwi, Reachable: false, Verdict: VerdictDead, CheckedAt: at.Add(time.Hour)})
	snap := c.Snapshot()
	if len(snap.Events) != 1 || snap.Events[0].Kind != EventUnreachable {
		t.Fatalf("snapshot events: %+v", snap.Events)
	}
	path := dir + "/catalog.json"
	if err := c.Save(path); err != nil {
		t.Fatal(err)
	}
	back, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Events) != 1 || back.Events[0].Key != "a" || back.Events[0].Verdict != VerdictDead {
		t.Fatalf("reloaded events: %+v", back.Events)
	}
}
