package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/decode"
	"github.com/chaim12345/sdrstream/go/internal/kiwisdr"
	"github.com/chaim12345/sdrstream/go/internal/web"
)

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
	old := directoryPreflightHTTPClient
	directoryPreflightHTTPClient = func() *http.Client { return &http.Client{Timeout: 8 * time.Second} }
	defer func() { directoryPreflightHTTPClient = old }()
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

func TestPreflightOutcomeMapsLocalVerdictsToSkips(t *testing.T) {
	for _, verdict := range []string{kiwisdr.VerdictBackoff, kiwisdr.VerdictBudget} {
		if got := preflightOutcome(verdict); got != (scanOutcome{skipped: true}) {
			t.Errorf("%s: got %+v, want skipped", verdict, got)
		}
	}
	if got := preflightOutcome(kiwisdr.VerdictTimeout); got != (scanOutcome{timedOut: true}) {
		t.Errorf("timeout: got %+v", got)
	}
	for _, verdict := range []string{kiwisdr.VerdictBusy, kiwisdr.VerdictDead} {
		if got := preflightOutcome(verdict); got != (scanOutcome{refused: true}) {
			t.Errorf("%s: got %+v, want refused", verdict, got)
		}
	}
}

func TestNormalizeStationOutcomeTreatsOwnDeadlineAsSkip(t *testing.T) {
	parent := context.Background()
	stationCtx, cancel := context.WithTimeout(parent, time.Millisecond)
	<-stationCtx.Done()
	defer cancel()
	got := normalizeStationOutcome(stationCtx, parent, scanOutcome{timedOut: true})
	if got != (scanOutcome{skipped: true}) {
		t.Fatalf("own session deadline must become a skip, got %+v", got)
	}
	// Parent cancellation is a shutdown, not a skip-worthy station result.
	parentCtx, parentCancel := context.WithCancel(context.Background())
	stationCtx2, cancel2 := context.WithTimeout(parentCtx, time.Minute)
	parentCancel()
	defer cancel2()
	got = normalizeStationOutcome(stationCtx2, parentCtx, scanOutcome{timedOut: true})
	if got != (scanOutcome{timedOut: true}) {
		t.Fatalf("parent shutdown must keep the original outcome, got %+v", got)
	}
	// A still-running station context changes nothing.
	stationCtx3, cancel3 := context.WithCancel(parent)
	defer cancel3()
	got = normalizeStationOutcome(stationCtx3, parent, scanOutcome{refused: true})
	if got != (scanOutcome{refused: true}) {
		t.Fatalf("live station ctx: got %+v", got)
	}
}

// TestJukeboxAPRSTempWAVRemoved: the jukebox APRS fast path writes a temp
// WAV for the decoder; every outcome - including a successful decode that
// records the capture - must remove it. Regression test for the leak where
// the decoded-and-continued path skipped the remove.
func TestJukeboxAPRSTempWAVRemoved(t *testing.T) {
	samples, rate, err := decode.ReadWAV("../../../tests/data/ref_aprs.wav")
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	capturesDir := t.TempDir()
	wavPath := filepath.Join(t.TempDir(), "sdrstream-aprs-test.wav")
	server := &web.Server{}
	states := []string{}
	update := func(state, result string) { states = append(states, state) }

	// The zero Server has no gallery directory, so AddLiveArtifact fails
	// and the call reports not-recorded - but the APRS decode itself
	// succeeded (the capture WAV proves it) and the temp file must still be
	// gone. That is the path that used to leak.
	tryJukeboxAPRS(server, samples, rate, wavPath, capturesDir, 144800000, "jukebox.spdns.eu", update)

	if _, err := os.Stat(wavPath); !os.IsNotExist(err) {
		t.Fatalf("temp WAV leaked: %v", err)
	}
	entries, err := os.ReadDir(capturesDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || !strings.HasSuffix(entries[0].Name(), ".wav") {
		t.Fatalf("capture not recorded: %v", entries)
	}
}

// TestJukeboxAPRSNoSignalAlsoRemovesTemp: a recording without APRS packets
// takes the early-exit path, which must remove the temp file too.
func TestJukeboxAPRSNoSignalAlsoRemovesTemp(t *testing.T) {
	samples := make([]float64, 8000)
	capturesDir := t.TempDir()
	wavPath := filepath.Join(t.TempDir(), "sdrstream-aprs-test.wav")
	server := &web.Server{}
	if tryJukeboxAPRS(server, samples, 8000, wavPath, capturesDir, 144800000, "jukebox.spdns.eu", func(string, string) {}) {
		t.Fatal("silence reported as decoded")
	}
	if _, err := os.Stat(wavPath); !os.IsNotExist(err) {
		t.Fatalf("temp WAV leaked on the no-decode path: %v", err)
	}
	if entries, _ := os.ReadDir(capturesDir); len(entries) != 0 {
		t.Fatalf("silence produced captures: %v", entries)
	}
}
