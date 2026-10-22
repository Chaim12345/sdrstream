package feeds

import (
	"compress/gzip"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPublicParsers(t *testing.T) {
	tests := []struct {
		name, body, label string
		count             int
	}{
		{"noaa-swpc", `[{"time_tag":"2026-10-22T03:00:00","Kp":3,"a_running":15,"station_count":6},{"time_tag":"2026-10-22T00:00:00","Kp":2}]`, "Planetary Kp 3", 1},
		{"wspr.live", `{"data":[{"time":"2026-10-22 06:42:00","tx_sign":"W1AW","rx_sign":"G1ABC","tx_loc":"FN31","frequency":14097125,"snr":-23}]}`, "W1AW", 1},
		{"sondehub", `{"S1":{"b":{"datetime":"2026-10-22T06:00:00Z","lat":40,"lon":0,"type":"RS41","alt":12345,"frequency":402.5},"a":{"datetime":"2026-10-22T05:00:00Z","lat":41,"lon":1}}}`, "S1", 1},
		{"digitraffic", `{"type":"FeatureCollection","features":[{"mmsi":123456789,"geometry":{"coordinates":[24.1,60.2]},"properties":{"timestampExternal":1792651457807,"sog":12.3,"cog":80}}]}`, "MMSI 123456789", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := PublicJSON{Source: tt.name}
			items, e := p.parse([]byte(tt.body))
			if e != nil || len(items) != tt.count {
				t.Fatalf("%v %+v", e, items)
			}
			if items[0].Label != tt.label || items[0].Time.IsZero() {
				t.Fatalf("bad item %+v", items[0])
			}
			if tt.name == "sondehub" && items[0].Lat != 40 {
				t.Fatal("did not select newest")
			}
			if tt.name == "digitraffic" && items[0].Lat != 60.2 {
				t.Fatal("GeoJSON lat/lon reversed")
			}
			if tt.name == "wspr.live" && (!items[0].HasPos || !strings.Contains(items[0].Detail, "14.097125")) {
				t.Fatal("grid/Hz conversion")
			}
			if p.Interval() < time.Minute {
				t.Fatal("impolite interval")
			}
		})
	}
}
func TestPublicGzipAndFailures(t *testing.T) {
	for _, compressed := range []bool{false, true} {
		t.Run(fmt.Sprint(compressed), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Accept-Encoding") != "gzip" || r.Header.Get("Digitraffic-User") == "" {
					t.Error("missing headers")
				}
				var body = `{"type":"FeatureCollection","features":[]}`
				if compressed {
					z := gzip.NewWriter(w)
					fmt.Fprint(z, body)
					z.Close()
				} else {
					fmt.Fprint(w, body)
				}
			}))
			defer srv.Close()
			p := PublicJSON{Source: "digitraffic", Endpoint: srv.URL}
			if _, e := p.Poll(context.Background()); e != nil {
				t.Fatal(e)
			}
		})
	}
	for _, status := range []int{429, 503} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
		p := PublicJSON{Source: "sondehub", Endpoint: srv.URL}
		if _, e := p.Poll(context.Background()); e == nil {
			t.Fatal("accepted HTTP error")
		}
		srv.Close()
	}
	for _, name := range []string{"wspr.live", "sondehub", "noaa-swpc", "digitraffic"} {
		p := PublicJSON{Source: name}
		if _, e := p.parse([]byte(`not json`)); e == nil {
			t.Fatal("accepted malformed JSON")
		}
	}
	p := PublicJSON{Source: "wspr.live"}
	if _, e := p.parse([]byte(`{}`)); e == nil {
		t.Fatal("accepted missing data")
	}
}

type cachePoller struct{ n atomic.Int32 }

func (p *cachePoller) Name() string            { return "cache-test" }
func (p *cachePoller) Kind() string            { return "test" }
func (p *cachePoller) Interval() time.Duration { return 5 * time.Millisecond }
func (p *cachePoller) Poll(ctx context.Context) ([]Item, error) {
	n := p.n.Add(1)
	if n == 2 {
		return nil, fmt.Errorf("offline")
	}
	return []Item{{ID: fmt.Sprint(n), Label: fmt.Sprint(n), Time: time.Now()}}, nil
}
func TestPollCacheReplacesAndClearsError(t *testing.T) {
	m := New(0, func(string, ...any) {})
	p := &cachePoller{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.RunPoller(ctx, p, time.Minute); close(done) }()
	defer func() { cancel(); <-done }()
	deadline := time.After(time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("poller timeout")
		default:
		}
		snap := m.Snapshot(10)
		if p.n.Load() >= 3 && len(snap.Sources) > 0 && snap.Sources[0].State == "live" {
			if snap.Sources[0].LastError != "" || len(snap.Items) != 1 {
				t.Fatalf("bad recovery %+v", snap)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
}
func TestSourceSamplesSurviveGlobalTruncation(t *testing.T) {
	m := New(100, nil)
	now := time.Now()
	for i := 0; i < 10; i++ {
		m.Add(Item{ID: fmt.Sprint(i), Label: "aircraft", Source: "adsb.lol", Time: now})
	}
	m.Add(Item{ID: "kp", Label: "Kp 3", Source: "noaa-swpc", Time: now.Add(-time.Hour)})
	s := m.Snapshot(1)
	if len(s.Items) != 1 {
		t.Fatal("global cap")
	}
	for _, source := range s.Sources {
		if source.Name == "noaa-swpc" && len(source.Samples) != 1 {
			t.Fatal("source hidden by aircraft")
		}
	}
}
