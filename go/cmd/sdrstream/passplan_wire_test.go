package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/passplan"
	"github.com/chaim12345/sdrstream/go/internal/tle"
	"github.com/chaim12345/sdrstream/go/internal/web"
)

// Real startup path: real tle cache dir, real state dir, real web handler.
// The element text is synthetic (checksummed ISS-shaped TLE), not live data.
const wireTLE = `ISS
1 25544U 98067A   26299.50000000  .00016717  00000-0  30000-3 0  9991
2 25544  51.6400 120.0000 0004000  60.0000 300.0000 15.50000000400009
`

func getPassplan(t *testing.T, srv *web.Server) map[string]interface{} {
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/passplan.json", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var out map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err, rec.Body.String())
	}
	return out
}

func TestStartPassplanRealCacheAndRestart(t *testing.T) {
	cacheDir, stateDir := t.TempDir(), t.TempDir()
	t.Setenv("SDRSTREAM_STATE_DIR", stateDir)
	// web.New runs the existing pass table load (network, real cache). Build
	// both servers BEFORE pointing the tle cache at the test dir.
	srv := web.New(t.TempDir(), t.TempDir())
	srv2 := web.New(t.TempDir(), t.TempDir())
	oldCache := tle.DefaultCacheDir
	tle.DefaultCacheDir = cacheDir
	defer func() { tle.DefaultCacheDir = oldCache }()
	if err := os.WriteFile(filepath.Join(cacheDir, "tle_amateur.txt"), []byte(wireTLE), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	svc, done := startPassplan(ctx, srv, 31.8, 35.0)
	deadline := time.Now().Add(10 * time.Second)
	var obs []interface{}
	for time.Now().Before(deadline) {
		out := getPassplan(t, srv)
		if out["enabled"] != true {
			t.Fatalf("not enabled: %v", out)
		}
		obs, _ = out["observations"].([]interface{})
		if len(obs) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	<-done
	if len(obs) == 0 {
		t.Fatalf("no observations from cached elements; info=%v", svc.InfoJSON())
	}
	statePath := filepath.Join(stateDir, "passplan_state.json")
	fi, err := os.Stat(statePath)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("state file: %v %v", fi, err)
	}
	// Restart with the element cache GONE: the saved plan must still be served.
	os.Remove(filepath.Join(cacheDir, "tle_amateur.txt"))
	ctx2, cancel2 := context.WithCancel(context.Background())
	_, done2 := startPassplan(ctx2, srv2, 31.8, 35.0)
	out := getPassplan(t, srv2)
	cancel2()
	<-done2
	obs2, _ := out["observations"].([]interface{})
	if len(obs2) != len(obs) {
		t.Fatalf("restart lost plan: %d vs %d", len(obs2), len(obs))
	}
}

func TestStartPassplanEmptyCacheHonest(t *testing.T) {
	t.Setenv("SDRSTREAM_STATE_DIR", t.TempDir())
	srv := web.New(t.TempDir(), t.TempDir()) // before the cache swap, see above
	oldCache := tle.DefaultCacheDir
	tle.DefaultCacheDir = t.TempDir()
	defer func() { tle.DefaultCacheDir = oldCache }()
	ctx, cancel := context.WithCancel(context.Background())
	svc, done := startPassplan(ctx, srv, 31.8, 35.0)
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(5 * time.Second)
	for svc.InfoJSON().(passplan.Info).LastRefresh.IsZero() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	out := getPassplan(t, srv)
	if out["enabled"] != true {
		t.Fatalf("%v", out)
	}
	if obs, _ := out["observations"].([]interface{}); len(obs) != 0 {
		t.Fatalf("must be empty without elements: %v info=%v", out, svc.InfoJSON())
	}

}
