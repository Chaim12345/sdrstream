package web

import (
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/monitor"
	"github.com/chaim12345/sdrstream/go/internal/testutil/sstvfixture"
	"github.com/chaim12345/sdrstream/go/internal/testutil/wefaxfixture"
	"github.com/chaim12345/sdrstream/go/internal/voice"
)

func f64(v float64) *float64 { return &v }

// TestSimulatedFeedUIServer serves the REAL UI over httptest with an
// honest simulated feed (one entry per verdict state: passed with its
// gallery twin, rejected, ungated, historical) behind a permanent
// "SIMULATED FEED TEST - no live reception" banner, so the feed
// semantics can be pixel-verified without seeding any production demo
// with synthetic-as-live data. It runs only with SDRSTREAM_SIM_UI=1
// and blocks until the stop file appears (default /tmp/sim_ui_stop).
// The pill expectation in this simulated scenario is 1 - a simulated
// test expectation, not a claim about real activity.
func TestSimulatedFeedUIServer(t *testing.T) {
	if os.Getenv("SDRSTREAM_SIM_UI") == "" {
		t.Skip("pixel-verification server; set SDRSTREAM_SIM_UI=1 to run")
	}
	now := time.Now().UTC().Truncate(time.Second)
	m := &monitor.Monitor{}
	passedID := m.AddActivity(monitor.Activity{Started: now, FrequencyHz: 14491234.7, Kind: "sstv", Mode: "sstv", LevelDB: f64(18.2), Source: "simulated"})
	rejectedID := m.AddActivity(monitor.Activity{Started: now.Add(-time.Minute), FrequencyHz: 7030000, Kind: "cw", Mode: "cw", LevelDB: f64(9.6), Detail: "CQ DE SIM", Source: "simulated"})
	m.AddActivity(monitor.Activity{Started: now.Add(-2 * time.Minute), FrequencyHz: 145800000, Kind: "unknown", LevelDB: f64(6.1), Source: "simulated"})
	m.RecordGate(passedID, true, "live gate passed")
	m.RecordGate(rejectedID, false, "text failed plausibility (simulated)")
	// A populated message log: twelve recent decodes spread across three
	// modes, then a slow trickle of new ones so paused/frozen versus live
	// behavior is observable in the browser. All simulated and labeled.
	modes := []string{"rtty", "cw", "psk31"}
	for i := 0; i < 12; i++ {
		m.AddActivity(monitor.Activity{Started: now.Add(time.Duration(-30-i) * time.Second), FrequencyHz: 14070000 + float64(i)*1000, Kind: "data", Mode: modes[i%3], LevelDB: f64(8 + float64(i%5)), Detail: fmt.Sprintf("CQ DE SIM%02d", i), Source: "simulated"})
	}
	go func() {
		for n := 12; ; n++ {
			time.Sleep(2500 * time.Millisecond)
			m.AddActivity(monitor.Activity{Started: time.Now().UTC(), FrequencyHz: 14070000 + float64(n%40)*1000, Kind: "data", Mode: modes[n%3], LevelDB: f64(9), Detail: fmt.Sprintf("CQ DE SIM%02d LIVE", n), Source: "simulated"})
		}
	}()
	s := &Server{}
	s.monitor = m
	// A decodable capture exercises POST /api/captures/decode from the
	// UI: synthetic gate-passing WEFAX audio with an honest sidecar.
	s.capturesDir = t.TempDir()
	faxSamples := wefaxfixture.Encode(wefaxfixture.Fixture{Rate: 12000, ImageLines: 280, Bars: true, LineSec: 0.5004, NoiseAmp: 0.3})
	if err := voice.WriteWAV(filepath.Join(s.capturesDir, "CAP_simfax.wav"), faxSamples, 12000); err != nil {
		t.Fatal(err)
	}
	if err := WriteCaptureMetadata(s.capturesDir, "CAP_simfax.wav", CaptureMetadata{Mode: "wefax", Receiver: "simulated", FrequencyHz: 13900000, Started: now.Add(-30 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	// A tall WEFAX-style chart (576x1400) exercises the lightbox zoom:
	// fit-to-screen shrinks it, full-size mode must scroll. Synthetic
	// stripes and blocks make the readability difference visible.
	s.galleryDir = t.TempDir()
	chart := image.NewGray(image.Rect(0, 0, 576, 1400))
	for y := 0; y < 1400; y++ {
		for x := 0; x < 576; x++ {
			switch {
			case y/40%2 == 0 && x/16%2 == 0:
				chart.SetGray(x, y, color.Gray{Y: 30})
			case (x-288)*(x-288)/140+(y-700)*(y-700)/90 < 900:
				chart.SetGray(x, y, color.Gray{Y: 200})
			default:
				chart.SetGray(x, y, color.Gray{Y: 120})
			}
		}
	}
	pngFile, err := os.Create(filepath.Join(s.galleryDir, "sim-wefax.png"))
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(pngFile, chart); err != nil {
		t.Fatal(err)
	}
	if err := pngFile.Close(); err != nil {
		t.Fatal(err)
	}
	// Small real PNGs for the live-tab seed cards so every card renders
	// an image (broken icons would hide placement proof).
	for _, small := range []struct {
		name  string
		phase int
	}{{"sim-twin.png", 0}, {"sim-legacy.png", 7}, {"sim-fixture.png", 13}} {
		img := image.NewGray(image.Rect(0, 0, 200, 120))
		for y := 0; y < 120; y++ {
			for x := 0; x < 200; x++ {
				img.SetGray(x, y, color.Gray{Y: uint8((x + y + small.phase) % 64 * 4)})
			}
		}
		f, err := os.Create(filepath.Join(s.galleryDir, small.name))
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, img); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	// Two more decodable captures with explicit source classes, next to
	// CAP_simfax.wav (no source line - the pre-field unknown case): the
	// re-decode placement proof needs one of each.
	for _, extra := range []struct{ name, source string }{
		{"CAP_simfax_live.wav", "live"},
		{"CAP_simfax_fix.wav", "test-fixture"},
	} {
		if err := voice.WriteWAV(filepath.Join(s.capturesDir, extra.name), faxSamples, 12000); err != nil {
			t.Fatal(err)
		}
		if err := WriteCaptureMetadata(s.capturesDir, extra.name, CaptureMetadata{Mode: "wefax", Receiver: "simulated", FrequencyHz: 13900000, Started: now.Add(-40 * time.Minute), Source: extra.source}); err != nil {
			t.Fatal(err)
		}
	}
	// A decodable SSTV capture exercises the same re-decode path for the
	// image mode whose live classification now ships: a synthesized
	// Martin M1 frame (color bars over a gradient), decoded for real and
	// gated for real, with honest test-fixture provenance. The click
	// proof shows the decoded frame, the gate's own reason, and the
	// fixture tab - what the user sees, not a code-level claim.
	sstvSamples := sstvfixture.EncodeMartinM1(func(x, y int) (uint8, uint8, uint8) {
		if y < sstvfixture.Height/2 {
			switch x * 6 / sstvfixture.Width {
			case 0:
				return 255, 0, 0
			case 1:
				return 255, 255, 0
			case 2:
				return 0, 255, 0
			case 3:
				return 0, 255, 255
			case 4:
				return 0, 0, 255
			default:
				return 255, 0, 255
			}
		}
		return uint8(x * 255 / (sstvfixture.Width - 1)), uint8(y * 255 / (sstvfixture.Height - 1)), 128
	})
	if err := voice.WriteWAV(filepath.Join(s.capturesDir, "CAP_simsstv.wav"), sstvSamples, sstvfixture.Rate); err != nil {
		t.Fatal(err)
	}
	if err := WriteCaptureMetadata(s.capturesDir, "CAP_simsstv.wav", CaptureMetadata{Mode: "sstv", Receiver: "simulated", FrequencyHz: 14230000, Started: now.Add(-25 * time.Minute), Source: "test-fixture"}); err != nil {
		t.Fatal(err)
	}
	s.liveGallery = []LiveGalleryItem{
		// Tall WEFAX-style chart for the lightbox zoom check. Kept on
		// the test-fixture tab so the documented pill expectation of 1
		// is unchanged; its string is honest fixture provenance, not a
		// reception claim.
		{Source: "test-fixture", Station: "simulated", Start: now, Kind: "image", Mode: "wefax", Name: "sim-wefax.png", URL: "/gallery/sim-wefax.png", FrequencyHz: 13900000, Verification: "synthetic WEFAX-style chart for zoom check - not a reception"},
		// Twin of the passed decode: merges into ONE counted entry.
		{Source: "live", Station: "simulated", Start: now, Kind: "image", Mode: "sstv", Name: "sim-twin.png", URL: "/gallery/sim-twin.png", FrequencyHz: 14491234, Verification: "simulated gate reason - not a reception"},
		// Legacy live item with no gate-owned verdict: historical.
		{Source: "live", Station: "simulated", Start: now.Add(-time.Hour), Kind: "image", Mode: "sstv", Name: "sim-legacy.png", URL: "/gallery/sim-legacy.png", FrequencyHz: 14491000},
		// Synthetic proof is not live provenance: never in the feed.
		{Source: "test-fixture", Station: "simulated", Start: now.Add(-2 * time.Hour), Kind: "image", Mode: "sstv", Name: "sim-fixture.png", URL: "/gallery/sim-fixture.png", Verification: "synthetic proof"},
	}
	inner := s.Handler()
	const banner = `<div style="background:#b91c1c;color:#fff;font:bold 16px/2.4 sans-serif;text-align:center;letter-spacing:.05em">SIMULATED FEED TEST - no live reception</div>`
	mux := http.NewServeMux()
	// The proof ledger needs a feeds manager the harness does not run;
	// serve three honest synthetic entries so the rxproof renderer can
	// be pixel-checked for each provenance class.
	mux.HandleFunc("/api/ledger.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		now := time.Now().UTC()
		json.NewEncoder(w).Encode(map[string]interface{}{"entries": []map[string]interface{}{
			{"source": "receiver", "kind": "audio", "id": "CAP_simfax_live.wav", "label": "wefax 13.900 MHz", "provider_time": now.Add(-30 * time.Minute), "decoded_by": "sdrstream", "receiver": "simulated", "evidence": "verified", "capture_source": "live", "ref": "/audio/CAP_simfax_live.wav"},
			{"source": "receiver", "kind": "audio", "id": "CAP_simfax_fix.wav", "label": "wefax 13.900 MHz", "provider_time": now.Add(-40 * time.Minute), "decoded_by": "sdrstream", "receiver": "simulated", "evidence": "unverified", "capture_source": "test-fixture", "ref": "/audio/CAP_simfax_fix.wav"},
			{"source": "receiver", "kind": "audio", "id": "CAP_simfax.wav", "label": "wefax 13.900 MHz", "provider_time": now.Add(-50 * time.Minute), "decoded_by": "sdrstream", "receiver": "simulated", "evidence": "unverified", "ref": "/audio/CAP_simfax.wav"},
		}})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			data, err := staticFS.ReadFile("static/index.html")
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			page := string(data)
			if i := strings.Index(page, "<body>"); i >= 0 {
				page = page[:i+6] + banner + page[i+6:]
			} else {
				page = banner + page
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, page)
			return
		}
		inner.ServeHTTP(w, r)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	fmt.Println("SIM_UI_URL=" + srv.URL)
	_ = os.WriteFile("/tmp/sim_ui_url", []byte(srv.URL), 0o644)
	stop := os.Getenv("SDRSTREAM_SIM_UI_STOP")
	if stop == "" {
		stop = "/tmp/sim_ui_stop"
	}
	for {
		if _, err := os.Stat(stop); err == nil {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}
