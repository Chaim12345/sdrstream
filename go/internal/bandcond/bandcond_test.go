package bandcond

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const sampleProduct = `[{"time_tag":"2026-10-22T00:00:00","Kp":2.33,"a_running":9,"station_count":6},{"time_tag":"2026-10-22T03:00:00","Kp":3.67,"a_running":22,"station_count":6}]`

func TestClassifyLabels(t *testing.T) {
	cases := []struct {
		kp         float64
		geomag     string
		band, want string
	}{
		{1.0, "quiet", "10m", "good"},
		{1.0, "quiet", "160m", "good"},
		{3.0, "unsettled", "10m", "fair"},
		{3.0, "unsettled", "40m", "good"},
		{4.0, "active", "80m", "fair"},
		{4.0, "active", "20m", "good"},
		{5.0, "minor storm", "80m", "poor"},
		{5.0, "minor storm", "20m", "fair"},
		{6.0, "storm", "40m", "poor"},
	}
	for _, tc := range cases {
		conditions := Classify(tc.kp, time.Now())
		if conditions.Geomag != tc.geomag {
			t.Errorf("Kp %.1f: geomag %q, want %q", tc.kp, conditions.Geomag, tc.geomag)
		}
		label, ok := conditions.LabelForHz(mustHz(t, tc.band))
		if !ok || label != tc.want {
			t.Errorf("Kp %.1f %s: label %q (ok=%v), want %q", tc.kp, tc.band, label, ok, tc.want)
		}
		if !conditions.Advisory {
			t.Error("snapshot must always carry the advisory flag")
		}
	}
}

func mustHz(t *testing.T, band string) float64 {
	t.Helper()
	for _, b := range hfBands {
		if b.Name == band {
			return (b.Low + b.High) / 2 * 1e6
		}
	}
	t.Fatalf("unknown band %s", band)
	return 0
}

func TestBandForHz(t *testing.T) {
	if band, ok := BandForHz(14.074e6); !ok || band != "20m" {
		t.Errorf("14.074 MHz: %q %v", band, ok)
	}
	if _, ok := BandForHz(518e3); ok {
		t.Error("518 kHz (NAVTEX) is not an amateur band; expected no band")
	}
	if band, ok := BandForHz(28.5e6); !ok || band != "10m" {
		t.Errorf("28.5 MHz: %q %v", band, ok)
	}
}

func TestFetch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, sampleProduct)
	}))
	defer server.Close()
	conditions, err := Fetch(context.Background(), server.URL, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if conditions.Kp != 3.67 {
		t.Errorf("Kp %.2f, want newest row 3.67", conditions.Kp)
	}
	if conditions.DataAt.Hour() != 3 {
		t.Errorf("data timestamp %s, want 03:00", conditions.DataAt)
	}
	if conditions.SourceURL != server.URL {
		t.Errorf("source url %q", conditions.SourceURL)
	}
}

func TestFetchFailure(t *testing.T) {
	if _, err := Fetch(context.Background(), "http://nonexistent.invalid/", 2*time.Second); err == nil {
		t.Fatal("expected an error for an unreachable host")
	}
}
