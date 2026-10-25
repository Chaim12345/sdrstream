package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
