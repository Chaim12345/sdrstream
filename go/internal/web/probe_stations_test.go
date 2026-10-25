package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/probe"
)

// probeFunc adapts a function to probe.Prober for tests.
type probeFunc func(rec *probe.Record)

func (f probeFunc) Probe(_ context.Context, rec *probe.Record) { f(rec) }

func TestProbeStationsEndpoint(t *testing.T) {
	catalog := probe.NewCatalog()
	catalog.Update(probe.Record{Key: "kiwi|a:8073", Kind: probe.KindKiwi, Name: "dead",
		Reachable: false, Verdict: probe.VerdictTimeout, CheckedAt: time.Now()})
	catalog.Update(probe.Record{Key: "kiwi|b:8073", Kind: probe.KindKiwi, Name: "alive",
		Reachable: true, Verdict: probe.VerdictOK, CheckedAt: time.Now(),
		Capability: probe.Capability{UsersMax: 4, FreqMaxHz: 30000000}})
	server := &Server{}
	server.SetProbeCatalog(catalog)
	req := httptest.NewRequest(http.MethodGet, "/api/station-catalog.json", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	var out struct {
		Enabled  bool `json:"enabled"`
		Stations []struct {
			Key       string `json:"key"`
			Reachable bool   `json:"reachable"`
			Name      string `json:"name"`
		} `json:"stations"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Enabled || len(out.Stations) != 2 {
		t.Fatalf("catalog not served: %s", rec.Body.String())
	}
	if !out.Stations[0].Reachable || out.Stations[0].Name != "alive" {
		t.Fatalf("reachable stations must sort first: %+v", out.Stations)
	}
}

// TestProbeStationsDuringSweep hammers the endpoint while a sweep writes
// the catalog; without the catalog lock this panics or trips -race.
func TestProbeStationsDuringSweep(t *testing.T) {
	catalog := probe.NewCatalog()
	server := &Server{}
	server.SetProbeCatalog(catalog)
	sched := &probe.Scheduler{
		Source: probe.Source{Manual: []probe.Record{
			{Key: "a", Kind: probe.KindKiwi}, {Key: "b", Kind: probe.KindKiwi},
		}},
		Prober: probeFunc(func(rec *probe.Record) {
			rec.Reachable, rec.Verdict, rec.CheckedAt = true, probe.VerdictOK, time.Now()
		}),
	}
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				_ = sched.Sweep(context.Background(), catalog)
			}
		}
	}()
	for i := 0; i < 100; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/station-catalog.json", nil)
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d", rec.Code)
		}
	}
	close(stop)
}

func TestProbeStationsDisabled(t *testing.T) {
	server := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/api/station-catalog.json", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	var out struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Enabled {
		t.Fatal("no catalog wired means enabled=false")
	}
}

// TestShellWiresStationBoard pins the station board integration: the page
// must load stationboard.js AFTER app.js (it uses the shared fetchJSON /
// fmtDate helpers), the view markup must exist, and the service worker
// shell must carry the new asset under a bumped cache name.
func TestShellWiresStationBoard(t *testing.T) {
	page, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	appIdx := strings.Index(string(page), `<script src="/static/app.js"></script>`)
	boardIdx := strings.Index(string(page), `<script src="/static/stationboard.js"></script>`)
	if appIdx < 0 || boardIdx < 0 {
		t.Fatalf("index.html must load app.js and stationboard.js (app at %d, board at %d)", appIdx, boardIdx)
	}
	if boardIdx < appIdx {
		t.Fatal("stationboard.js must load after app.js")
	}
	for _, marker := range []string{`data-view="stationboard"`, `id="view-stationboard"`, `id="board-list"`, `id="board-disabled"`, `id="board-refresh"`, `Station probing is not enabled on this server.`, `--probe-directory`} {
		if !strings.Contains(string(page), marker) {
			t.Fatal(marker)
		}
	}
	sw, err := staticFS.ReadFile("static/sw.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sw), "'/static/stationboard.js'") {
		t.Fatal("service worker shell missing /static/stationboard.js")
	}
	if !strings.Contains(string(sw), "sdrstream-shell-v5") {
		t.Fatal("service worker CACHE not bumped for the station board shell change")
	}
	if _, err := staticFS.ReadFile("static/stationboard.js"); err != nil {
		t.Fatal("static/stationboard.js not embedded")
	}
}

func TestProbeStationsExposesEventsNewestFirst(t *testing.T) {
	catalog := probe.NewCatalog()
	at := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	catalog.Update(probe.Record{Key: "a", Kind: probe.KindKiwi, Reachable: true, CheckedAt: at})
	catalog.Update(probe.Record{Key: "a", Kind: probe.KindKiwi, Reachable: false, Verdict: probe.VerdictTimeout, CheckedAt: at.Add(time.Hour)})
	catalog.Update(probe.Record{Key: "a", Kind: probe.KindKiwi, Reachable: true, Verdict: probe.VerdictOK, CheckedAt: at.Add(2 * time.Hour)})
	server := &Server{}
	server.SetProbeCatalog(catalog)

	req := httptest.NewRequest(http.MethodGet, "/api/station-catalog.json", nil)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	var body struct {
		Events []probe.Event `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Events) != 2 {
		t.Fatalf("events = %+v", body.Events)
	}
	if body.Events[0].Kind != probe.EventRecovered || body.Events[1].Kind != probe.EventUnreachable {
		t.Fatalf("want newest-first [recovered, unreachable], got %+v", body.Events)
	}
}
