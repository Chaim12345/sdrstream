package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
			probed[rec.Key]++
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
	for k, n := range probed {
		if n != 1 {
			t.Fatalf("station %s probed %d times", k, n)
		}
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
