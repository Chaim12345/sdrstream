package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/bandcond"
	"github.com/chaim12345/sdrstream/go/internal/sources"
)

func TestSourcesOnDemandSweep(t *testing.T) {
	var calls int64
	server := &Server{
		healthAll: func(ctx context.Context, opts sources.Options) []sources.Result {
			atomic.AddInt64(&calls, 1)
			return []sources.Result{{Name: "receiverbook", Kind: "receiver-directory",
				Status: sources.StatusLive, Detail: "HTTP 200"}}
		},
	}
	handler := server.Handler()

	// First request triggers the sweep; results arrive once it finishes.
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest("GET", "/api/sources.json", nil))
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt64(&calls) == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		server.mu.Lock()
		running := server.sourcesRunning
		server.mu.Unlock()
		if !running {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest("GET", "/api/sources.json", nil))
	var response sourcesResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || response.Results[0].Status != sources.StatusLive {
		t.Fatalf("unexpected results %+v", response.Results)
	}
	// A plain poll within the rate-limit window must not re-sweep.
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/sources.json?refresh=1", nil))
	time.Sleep(50 * time.Millisecond)
	if got := atomic.LoadInt64(&calls); got != 1 {
		t.Fatalf("sweep ran %d times, want 1 (rate limit)", got)
	}
}

func TestBandConditionsServedAndCached(t *testing.T) {
	var calls int64
	server := &Server{
		fetchConditions: func(ctx context.Context) (bandcond.Conditions, error) {
			atomic.AddInt64(&calls, 1)
			return bandcond.Classify(2.33, time.Now().UTC()), nil
		},
	}
	handler := server.Handler()
	get := func() bandcondResponse {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest("GET", "/api/bandconditions.json", nil))
		var response bandcondResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	first := get()
	if !first.Advisory || first.Kp != 2.33 || len(first.Bands) != 10 {
		t.Fatalf("unexpected response %+v", first)
	}
	second := get()
	if atomic.LoadInt64(&calls) != 1 {
		t.Fatalf("NOAA fetched %d times, want 1 (30-minute cache)", calls)
	}
	if second.Stale || second.Err != "" {
		t.Errorf("fresh cache must not be marked stale: %+v", second)
	}
}

func TestBandConditionsFailureKeepsAdvisoryShape(t *testing.T) {
	server := &Server{
		fetchConditions: func(ctx context.Context) (bandcond.Conditions, error) {
			return bandcond.Conditions{}, errors.New("noaa unreachable")
		},
	}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/api/bandconditions.json", nil))
	var response bandcondResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Err == "" || !response.Advisory || response.Bands == nil {
		t.Fatalf("error response must stay well-formed: %+v", response)
	}
}

func TestScanTargetsAnnotatedWithBandCondition(t *testing.T) {
	server := &Server{
		cond:   bandcond.Classify(1.0, time.Now().UTC()),
		condAt: time.Now(),
		scans: []ScanStatus{
			{Station: "rx1", FrequencyHz: 14.074e6, Mode: "USB", State: "listening"},
			{Station: "rx2", FrequencyHz: 518e3, Mode: "NAVTEX", State: "listening"},
		},
	}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/api/scans.json", nil))
	var metrics ScanMetrics
	if err := json.Unmarshal(recorder.Body.Bytes(), &metrics); err != nil {
		t.Fatal(err)
	}
	var annotated, plain int
	for _, station := range metrics.Stations {
		if station.BandCondition != "" {
			annotated++
			if !strings.Contains(station.BandCondition, "20m") {
				t.Errorf("14.074 MHz annotated as %q, want 20m", station.BandCondition)
			}
		} else {
			plain++
		}
	}
	if annotated != 1 || plain != 1 {
		t.Errorf("annotated=%d plain=%d, want 1/1", annotated, plain)
	}
}

