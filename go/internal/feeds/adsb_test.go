package feeds

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

const adsbSample = `{"ac":[
{"hex":"744821","flight":"RJA134  ","alt_baro":27000,"gs":428.4,"squawk":"1163","lat":32.67,"lon":34.27,"seen_pos":0.5,"t":"A20N"},
{"hex":"abc123","flight":"","alt_baro":"ground","lat":0,"lon":0}
],"msg":"No error"}`

func TestADSBPoll(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/point/32.0800/34.7800/250" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Write([]byte(adsbSample))
	}))
	defer srv.Close()
	a := &ADSB{Base: srv.URL, Lat: 32.08, Lon: 34.78, Radius: 250}
	items, err := a.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("want 2 aircraft, got %d", len(items))
	}
	first := items[0]
	if first.Label != "RJA134" || !first.HasPos || first.Ref == "" {
		t.Fatalf("bad first item: %+v", first)
	}
	if first.Detail == "" || first.ID != "adsb|744821" {
		t.Fatalf("missing detail/id: %+v", first)
	}
	if items[1].Label != "ABC123" || items[1].HasPos {
		t.Fatalf("fallback labelling failed: %+v", items[1])
	}
}
