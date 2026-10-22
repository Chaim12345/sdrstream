package feeds

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestSondeBootstrapOnlyOnceAcrossReconnects(t *testing.T) {
	var boot atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bootstrap" {
			boot.Add(1)
			fmt.Fprint(w, `{"S1":{"t":{"datetime":"2026-10-22T06:00:00Z","lat":1,"lon":2}}}`)
			return
		}
		w.WriteHeader(503)
	}))
	defer srv.Close()
	s := SondeHub{Bootstrap: &PublicJSON{Source: "sondehub", Endpoint: srv.URL + "/bootstrap"}, BrokerEndpoint: srv.URL + "/broker"}
	count := 0
	for i := 0; i < 2; i++ {
		if e := s.Run(context.Background(), func(Item) { count++ }); e == nil {
			t.Fatal("expected broker failure")
		}
	}
	if boot.Load() != 1 || count != 1 {
		t.Fatalf("repolled history: bootstrap=%d emitted=%d", boot.Load(), count)
	}
}
func TestSondeMQTTTelemetry(t *testing.T) {
	for _, b := range []string{`{"serial":"S1","datetime":"2026-10-22T06:00:00Z","lat":0,"lon":0,"alt":1234}`, `[{"serial":"S1","datetime":"2026-10-22T06:00:00Z","lat":0,"lon":0,"alt":1234}]`} {
		items := parseSondeMessage([]byte(b))
		if len(items) != 1 || items[0].Label != "S1" || !items[0].HasPos {
			t.Fatalf("%+v", items)
		}
	}
	for _, b := range []string{`{}`, `invalid`, `{"serial":"S1","datetime":"bad","lat":0,"lon":0}`, `{"serial":"S1","datetime":"2026-10-22T06:00:00Z","lat":100,"lon":0}`} {
		if len(parseSondeMessage([]byte(b))) != 0 {
			t.Fatal("accepted invalid telemetry")
		}
	}
}
