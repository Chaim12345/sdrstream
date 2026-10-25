package probe

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/chaim12345/sdrstream/go/internal/kiwisdr"
)

func TestKiwiDirectorySourceCarriesListingHints(t *testing.T) {
	page := `var kiwisdr_com=[
{"url":"http://sdr.example.com:8073","name":"Test Kiwi","antenna":"vertical","gps":"(52.1, 5.1)","bands":"0-30000000","users":"1","users_max":"4","sw_version":"1.800","loc":"Amsterdam, NL","updated":"2026-10-24","status":"active"}
];`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, page)
	}))
	defer srv.Close()
	loader := kiwisdr.NewLoader("")
	loader.URL = srv.URL
	recs, err := KiwiDirectorySource(loader)(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("want 1 record, got %d: %+v", len(recs), recs)
	}
	rec := recs[0]
	if rec.Kind != KindKiwi || rec.Key != "kiwi|sdr.example.com:8073" {
		t.Fatalf("identity: %+v", rec)
	}
	if rec.Lat != 52.1 || rec.Lon != 5.1 || rec.Name != "Test Kiwi" {
		t.Fatalf("listing fields: %+v", rec)
	}
	if rec.Capability.FreqMinHz != 0 || rec.Capability.FreqMaxHz != 30000000 {
		t.Fatalf("bands hint: %+v", rec.Capability)
	}
	if rec.Capability.UsersMax != 4 || rec.Capability.Antenna != "vertical" || rec.Capability.Location != "Amsterdam, NL" || rec.Capability.Version != "1.800" {
		t.Fatalf("listing hints: %+v", rec.Capability)
	}
	if rec.ListedOffline || rec.ListedUpdated != "2026-10-24" {
		t.Fatalf("listed state: %+v", rec)
	}
}

func TestParseBands(t *testing.T) {
	for _, tc := range []struct {
		in     string
		lo, hi int64
		ok     bool
	}{
		{"0-30000000", 0, 30000000, true},
		{"10000-30000000", 10000, 30000000, true},
		{"", 0, 0, false},
		{"30000000-0", 0, 0, false},
		{"junk", 0, 0, false},
	} {
		lo, hi, ok := parseBands(tc.in)
		if lo != tc.lo || hi != tc.hi || ok != tc.ok {
			t.Errorf("parseBands(%q) = %d,%d,%v", tc.in, lo, hi, ok)
		}
	}
}
