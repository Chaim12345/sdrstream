package feeds

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const potaSample = `[{"spotId": 59267220, "activator": "CT2KNS/P", "frequency": "7174", "mode": "SSB", "reference": "PT-0048", "spotTime": "2026-10-22T06:58:25", "spotter": "EB5AP", "comments": "Good signal", "name": "Cerro da Cabeca Natura 2000", "latitude": 37.1125, "longitude": -7.78194}]`

func TestPOTAPoll(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/spots" {
			t.Errorf("wrong path: %s", r.URL.Path)
		}
		w.Write([]byte(potaSample))
	}))
	defer srv.Close()
	p := &POTA{Base: srv.URL}
	items, err := p.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 spot, got %+v", items)
	}
	it := items[0]
	if it.Source != "pota" || it.Kind != "parks" || !it.HasPos {
		t.Fatalf("bad item: %+v", it)
	}
	if !strings.Contains(it.Label, "CT2KNS/P") || !strings.Contains(it.Detail, "PT-0048") {
		t.Fatalf("bad content: %+v", it)
	}
	if it.Time.Hour() != 6 {
		t.Fatalf("time parse failed: %v", it.Time)
	}
}

const sotaSample = `[{"id":414338,"timeStamp":"2026-10-22T06:53:15.365148Z","activatorCallsign":"HL1/WX7OR","callsign":"WX7OR","comments":"Try HF first","frequency":14.06,"mode":"CW","summitCode":"HL/SL-004","summitName":"Inwangsan","type":"QRT","latitude":37.5848,"longitude":126.958}]`

func TestSOTAPoll(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/spots/25/all/all" {
			t.Errorf("wrong path: %s", r.URL.Path)
		}
		w.Write([]byte(sotaSample))
	}))
	defer srv.Close()
	s := &SOTA{Base: srv.URL}
	items, err := s.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 spot, got %+v", items)
	}
	it := items[0]
	if it.Source != "sota" || it.Kind != "parks" {
		t.Fatalf("bad item: %+v", it)
	}
	if !strings.Contains(it.Label, "HL1/WX7OR") || !strings.Contains(it.Label, "14.060 MHz") || !strings.Contains(it.Detail, "HL/SL-004") {
		t.Fatalf("bad content: %+v", it)
	}
	if it.Time.Hour() != 6 {
		t.Fatalf("time parse failed: %v", it.Time)
	}
}
