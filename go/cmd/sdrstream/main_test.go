package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/chaim12345/sdrstream/go/internal/web"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"syscall"
	"testing"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/kiwisdr"
)

// A descriptor exhaustion on this machine must not look like a receiver
// refusal: main.go used to blocklist every healthy host it dialled during
// an EMFILE spike ("too many open files" matched the "too many" refusal
// bait) and backed the concurrency ramp down on top of it.
func TestClassifyScanFailureLocalResourceIsNotReceiverRefusal(t *testing.T) {
	emfile := &net.OpError{Op: "dial", Err: syscall.EMFILE}
	out := classifyScanFailure(emfile)
	if out.refused || out.timedOut {
		t.Fatalf("EMFILE classified as receiver failure: %+v", out)
	}
	if !out.localResource {
		t.Fatalf("EMFILE not marked localResource: %+v", out)
	}
	if out = classifyScanFailure(fmt.Errorf("dial tcp 1.2.3.4:8073: bind: too many open files")); !out.localResource || out.refused {
		t.Fatalf("textual EMFILE: %+v", out)
	}
	// Receiver-side refusals keep their meaning, including Kiwi-style
	// "too many users".
	if out = classifyScanFailure(errors.New("connection refused")); !out.refused || out.localResource {
		t.Fatalf("refused: %+v", out)
	}
	if out = classifyScanFailure(errors.New("server busy: too many users")); !out.refused || out.localResource {
		t.Fatalf("busy receiver: %+v", out)
	}
	if out = classifyScanFailure(fmt.Errorf("test")); out.refused || out.timedOut || out.localResource {
		t.Fatalf("generic error: %+v", out)
	}
}

func TestStationBlocklistPersistsAcrossReload(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SDRSTREAM_STATE_DIR", dir)
	until := time.Now().Add(30 * time.Minute).UTC().Truncate(time.Second)
	want := map[string]stationBlock{"radio.example": {Reason: "connection refused", Until: until}}
	saveBlocklist(want)
	got := loadBlocklist()
	if got["radio.example"].Reason != want["radio.example"].Reason || !got["radio.example"].Until.Equal(until) {
		t.Fatalf("blocklist reload = %+v, want %+v", got, want)
	}
}

func TestNewScanConfigCapsRampAndStartsAtConfiguredStep(t *testing.T) {
	cfg, err := newScanConfig(49, 100, "49,100", .15, .30, .70, .90, 20, 100, 40, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.start != 49 || cfg.maximum != 100 || len(cfg.ramp) != 2 || cfg.ramp[0] != 49 || cfg.ramp[1] != 100 {
		t.Fatalf("unexpected scan config: %+v", cfg)
	}
	if _, err := newScanConfig(101, 100, "20,50", .15, .30, .70, .90, 20, 100, 40, 5*time.Minute); err == nil {
		t.Fatal("start above maximum accepted")
	}
	if _, err := newScanConfig(20, scanHardMaxConnections+1, "20,50", .15, .30, .70, .90, 20, 100, 40, 5*time.Minute); err == nil {
		t.Fatal("maximum above hard cap accepted")
	}
	if _, err := newScanConfig(20, scanHardMaxConnections, "20,50", .15, .30, .70, .90, 20, 100, 40, 5*time.Minute); err != nil {
		t.Fatalf("hard cap maximum rejected: %v", err)
	}
}

func TestClassifyScanFailure(t *testing.T) {
	if got := classifyScanFailure(errors.New("connection refused")); !got.refused || got.timedOut {
		t.Fatalf("refusal classification: %+v", got)
	}
	if got := classifyScanFailure(errors.New("listen timeout")); !got.timedOut {
		t.Fatalf("timeout classification: %+v", got)
	}
}

func TestScanConfigTargetLimits(t *testing.T) {
	base := func(limit int, session time.Duration) error {
		_, err := newScanConfig(49, 100, "49,100", .15, .30, .70, .90, 20, 100, limit, session)
		return err
	}
	if err := base(40, 5*time.Minute); err != nil {
		t.Fatal(err)
	}
	if base(41, 5*time.Minute) == nil {
		t.Fatal("target limit above 40 accepted")
	}
	if base(40, 6*time.Minute) == nil {
		t.Fatal("station session above five minutes accepted")
	}
}

func TestScanStatusIndexesRemainStablePastDisplayWindow(t *testing.T) {
	scansMu.Lock()
	old := scans
	scans = nil
	scansMu.Unlock()
	defer func() { scansMu.Lock(); scans = old; scansMu.Unlock() }()
	server := web.New("", "")
	for i := 0; i < 600; i++ {
		addScan(server, web.ScanStatus{Station: fmt.Sprintf("rx-%d", i), FrequencyHz: 145500000, Mode: "fm", State: "connecting"})
	}
	setScanResult(server, "rx-599|145500000|fm", "timeout", "deadline")
	scansMu.Lock()
	got := scans[len(scans)-1]
	scansMu.Unlock()
	if got.Station != "rx-599" || got.State != "timeout" {
		t.Fatalf("last stable status = %+v", got)
	}
}

func TestRampUsesTimeoutsForAdvanceAndRefusalsForBackoff(t *testing.T) {
	cfg, err := newScanConfig(20, 100, "20,49,100", .15, .30, .70, .90, 20, 100, 40, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if got := nextRampStage(0, 3, 0, .20, .2, 2, cfg); got != 0 {
		t.Fatalf("advanced despite timeout/error rate above gate: %d", got)
	}
	if got := nextRampStage(0, 3, 0, .14, .2, 2, cfg); got != 1 {
		t.Fatalf("did not advance below configured error rate: %d", got)
	}
	if got := nextRampStage(2, 3, .31, .31, .2, 2, cfg); got != 1 {
		t.Fatalf("did not back off on excessive refusal rate: %d", got)
	}
	// Load no longer sheds a step inside one window: that decision belongs
	// to the load guard, which requires sustained overload. A hot window
	// only blocks the advance.
	if got := nextRampStage(2, 3, .1, .1, .91, 2, cfg); got != 2 {
		t.Fatalf("load step-down leaked into the window gate (guard owns it): %d", got)
	}
}

func TestPreflightDirectoryStationIdentity(t *testing.T) {
	tests := []struct {
		kind, body string
		ok         bool
	}{
		{"OpenWebRX", `<html><title>OpenWebRX</title></html>`, true},
		{"OpenWebRX", `<html>plain site</html>`, false},
		{"WebSDR", `<script src="websdr.js"></script>`, true},
		{"WebSDR", `<html>plain site</html>`, false},
	}
	for _, tt := range tests {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, tt.body) }))
		err := preflightDirectoryStation(context.Background(), tt.kind, server.URL)
		server.Close()
		if (err == nil) != tt.ok {
			t.Errorf("%s body %q: err=%v, want ok=%t", tt.kind, tt.body, err, tt.ok)
		}
	}
}

func TestShutdownFailuresDoNotBlockAndBlocklistCleanupPreservesReceiverFailures(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, reason := range []string{"use of closed network connection", "context canceled", "EOF: use of closed network connection"} {
		if shouldBlockScanFailure(context.Background(), reason) {
			t.Errorf("shutdown failure should not block: %q", reason)
		}
	}
	if shouldBlockScanFailure(ctx, "receiver refused") {
		t.Fatal("canceled scan context should not block")
	}
	if !shouldBlockScanFailure(context.Background(), "receiver refused") || !shouldBlockScanFailure(context.Background(), "preflight HTTP status 403 Forbidden") || !shouldBlockScanFailure(context.Background(), "context deadline exceeded") {
		t.Fatal("genuine receiver refusal, 403, and timeout must remain blockable")
	}
	input := map[string]stationBlock{
		"shutdown.example": {Reason: "use of closed network connection", Until: time.Now().Add(time.Hour)},
		"cancel.example":   {Reason: "context canceled", Until: time.Now().Add(time.Hour)},
		"refused.example":  {Reason: "connection refused", Until: time.Now().Add(time.Hour)},
		"403.example":      {Reason: "preflight HTTP status 403 Forbidden", Until: time.Now().Add(time.Hour)},
		"timeout.example":  {Reason: "context deadline exceeded", Until: time.Now().Add(time.Hour)},
	}
	cleaned, removed := cleanShutdownBlocklistEntries(input)
	if removed != 2 || len(cleaned) != 3 {
		t.Fatalf("cleanup removed=%d entries=%v", removed, cleaned)
	}
	for _, host := range []string{"refused.example", "403.example", "timeout.example"} {
		if _, ok := cleaned[host]; !ok {
			t.Errorf("genuine receiver failure %q was removed", host)
		}
	}
}

func TestAppendUsableKiwiStationsDedupesByHostPortAndSlots(t *testing.T) {
	existing := []string{"OpenWebRX|http://radio.example:8073/", "KiwiSDR|http://dup.example:8073"}
	candidates := []kiwisdr.Receiver{
		{Host: "radio.example", Port: 8073, URL: "http://radio.example:8073", Users: 0, UsersMax: 3}, // same endpoint, don't add another client
		{Host: "dup.example", Port: 8073, URL: "http://dup.example:8073/", Users: 0, UsersMax: 4},
		{Host: "full.example", Port: 8073, URL: "http://full.example:8073", Users: 4, UsersMax: 4},
		{Host: "free.example", Port: 8073, URL: "http://free.example:8073", Users: 1, UsersMax: 4},
	}
	got := appendUsableKiwiStations(existing, candidates)
	if len(got) != 3 {
		t.Fatalf("stations=%v; want 3 after host:port dedupe and slot filtering", got)
	}
	if got[2] != "KiwiSDR|http://free.example:8073" {
		t.Fatalf("added station %q", got[2])
	}
}

// TestLooksLikeKiwiDoesNotClaimOpenWebRX pins a fleet-scan failure: OpenWebRX
// also defaults to port 8073, and the old ":8073" heuristic sent the
// OpenWebRX station to the Kiwi preflight, which built http://http//host/status
// and blocklisted a healthy receiver as "refused".
func TestLooksLikeKiwiDoesNotClaimOpenWebRX(t *testing.T) {
	cases := []struct {
		host string
		want bool
	}{
		{"http://sdr.ladar.club:8073", false},
		{"http://example.com:8073", false},
		{"http://kiwisdr.example:8073", true},
		{"https://kiwi.example:8073/", true},
	}
	for _, test := range cases {
		if got := looksLikeKiwi(test.host); got != test.want {
			t.Errorf("looksLikeKiwi(%q) = %v, want %v", test.host, got, test.want)
		}
	}
}
