package feeds

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const wsprLiveSample = `{"time":"2026-10-22 06:58:00","band":7,"rx_sign":"W0EGR","rx_loc":"EM79","tx_sign":"KF4JU","tx_loc":"FM05","distance":1936,"frequency":7040183,"snr":-19,"power":37,"drift":0}
{"time":"2026-10-22 06:58:00","band":14,"rx_sign":"K2TAV","rx_loc":"FN33","tx_sign":"4X1RF","tx_loc":"KM72","distance":9123,"frequency":14097169,"snr":-17,"power":33,"drift":1}
`

func TestWSPRLivePoll(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Query().Get("query"), "FROM wspr.rx") {
			t.Errorf("wrong query: %s", r.URL.RawQuery)
		}
		w.Write([]byte(wsprLiveSample))
	}))
	defer srv.Close()
	w := &WSPRLive{Base: srv.URL}
	items, err := w.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("want 2 spots, got %+v", items)
	}
	it := items[1]
	if it.Source != "wspr.live" || it.Kind != "digimode" {
		t.Fatalf("bad item: %+v", it)
	}
	if !strings.Contains(it.Label, "4X1RF") || !strings.Contains(it.Label, "14.097 MHz") || !strings.Contains(it.Label, "20m") {
		t.Fatalf("bad label: %+v", it)
	}
	if !strings.Contains(it.Detail, "K2TAV") || !strings.Contains(it.Detail, "-17 dB") {
		t.Fatalf("bad detail: %+v", it)
	}
	if it.Time.Hour() != 6 {
		t.Fatalf("time parse failed: %v", it.Time)
	}
}

func TestWSPRLiveEmptyIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("\n"))
	}))
	defer srv.Close()
	w := &WSPRLive{Base: srv.URL}
	if _, err := w.Poll(context.Background()); err == nil {
		t.Fatal("empty window should be an error so the manager retries")
	}
}
