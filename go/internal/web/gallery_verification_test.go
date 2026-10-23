package web

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/monitor"
)

// Manifest round-trip: a recorded gate reason survives reload, and an
// item saved WITHOUT one reloads with an empty verification - the
// reload path must not manufacture proof for historical items.
func TestLiveGalleryReloadPreservesVerificationSemantics(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"live-a.png", "live-b.png"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("png"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reason := "sstv reception verified: Martin M1 VIS 0x2C (parity-checked); sync coasting bounded 0/256"
	items := []LiveGalleryItem{
		{Source: "live", Satellite: "Live reception", Station: "r1", Start: time.Now().UTC(), Kind: "image", Name: "live-a.png", URL: "/gallery/live-a.png", Verification: reason},
		{Source: "live", Satellite: "Live reception", Station: "r1", Start: time.Now().UTC(), Kind: "image", Name: "live-b.png", URL: "/gallery/live-b.png"},
	}
	data, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, liveGalleryIndexFile), data, 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Server{galleryDir: dir}
	s.loadLiveGallery()
	if len(s.liveGallery) != 2 {
		t.Fatalf("reloaded %d items, want 2", len(s.liveGallery))
	}
	byName := map[string]LiveGalleryItem{}
	for _, it := range s.liveGallery {
		byName[it.Name] = it
	}
	if got := byName["live-a.png"].Verification; got != reason {
		t.Errorf("recorded reason lost on reload: %q", got)
	}
	if got := byName["live-b.png"].Verification; got != "" {
		t.Errorf("historical item gained proof on reload: %q", got)
	}
}

// The activity feed merges gallery items by source-owned class: only
// a gate-verified live item merges as current activity; live and
// legacy items without a gate-owned verdict merge as historical;
// test fixtures and other non-receiver sources never enter the feed.
func TestHandleActivityMergesBySourceClass(t *testing.T) {
	s := &Server{}
	now := time.Now().UTC()
	s.liveGallery = []LiveGalleryItem{
		{Source: "live", Station: "r1", Start: now, Kind: "image", Mode: "sstv", Name: "verified.png", Verification: "sstv reception verified: test reason"},
		{Source: "live", Station: "r1", Start: now.Add(-time.Hour), Kind: "image", Mode: "sstv", Name: "legacy.png"},
		// A reason string on a legacy (source-less) entry is not a
		// gate verdict: the verdict is written with the source.
		{Station: "r1", Start: now.Add(-2 * time.Hour), Kind: "image", Mode: "sstv", Name: "legacy-string.png", Verification: "unattributed string"},
		// Synthetic proof is not live provenance: test fixtures are
		// excluded from receiver activity whatever they carry.
		{Source: "test-fixture", Station: "synthetic", Start: now.Add(-3 * time.Hour), Kind: "image", Mode: "sstv", Name: "fixture.png", Verification: "synthetic fixture: checks pass (not a reception)"},
		{Source: "satnogs", Station: "satnogs 145.800", Start: now.Add(-4 * time.Hour), Kind: "image", Mode: "sstv", Name: "import.png"},
	}
	req := httptest.NewRequest("GET", "/api/activity.json", nil)
	rec := httptest.NewRecorder()
	s.handleActivity(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	var items []monitor.Activity
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("%d feed items, want 3 (verified + 2 historical; foreign excluded)", len(items))
	}
	var verified, historical int
	for _, a := range items {
		if a.Historical {
			historical++
			if a.Verification != "" {
				t.Errorf("historical entry %q carried a verification string into the feed", a.Detail)
			}
		} else {
			verified++
			if a.Verification != "sstv reception verified: test reason" {
				t.Errorf("verified entry lost its gate reason: %q", a.Verification)
			}
		}
	}
	if verified != 1 || historical != 2 {
		t.Errorf("verified=%d historical=%d, want 1 and 2", verified, historical)
	}
}

// galleryLiveClass pins the classification itself: the gate verdict
// exists only where the live writer recorded source and reason in one
// write; unknown sources are never inferred live.
func TestGalleryLiveClass(t *testing.T) {
	cases := []struct {
		source, verification string
		want                 galleryLiveClass
	}{
		{"live", "reason", galleryLiveVerified},
		{"live", "", galleryLiveHistorical},
		{"", "unattributed string", galleryLiveHistorical},
		{"", "", galleryLiveHistorical},
		{"test-fixture", "synthetic proof", galleryForeign},
		{"test-fixture", "", galleryForeign},
		{"satnogs", "", galleryForeign},
		{"future-unknown-source", "reason", galleryForeign},
	}
	for _, c := range cases {
		it := LiveGalleryItem{Source: c.source, Verification: c.verification}
		if got := it.galleryLiveClass(); got != c.want {
			t.Errorf("source %q verification %q: class %d, want %d", c.source, c.verification, got, c.want)
		}
	}
}

// A decode the monitor reported and the gallery also saved must merge
// into ONE feed entry: the gallery write truncates the frequency to
// whole Hz and uses a different kind vocabulary ("sstv" vs "image"),
// so the dedupe key is start + whole-Hz frequency + detail only.
func TestActivityDedupeKeyMatchesGalleryTwin(t *testing.T) {
	start := time.Now().UTC()
	a := monitor.Activity{Started: start, FrequencyHz: 14491234.7, Kind: "sstv"}
	it := LiveGalleryItem{Source: "live", Start: start, FrequencyHz: int64(a.FrequencyHz), Kind: "image", Text: a.Detail}
	mk := activityDedupeKey(a.Started.UnixNano(), int64(a.FrequencyHz), a.Detail)
	gk := activityDedupeKey(it.Start.UnixNano(), it.FrequencyHz, it.Text)
	if mk != gk {
		t.Errorf("twin keys differ: monitor %q gallery %q", mk, gk)
	}
	other := activityDedupeKey(it.Start.UnixNano(), it.FrequencyHz, "different detail")
	if mk == other {
		t.Error("distinct decodes collided on the dedupe key")
	}
}
