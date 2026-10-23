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

// The activity feed merges gallery items: entries with a recorded
// gate reason keep it, entries without one are marked historical so
// the UI never counts them as current gate-passed traffic.
func TestHandleActivityMarksLegacyGalleryEntries(t *testing.T) {
	s := &Server{}
	s.liveGallery = []LiveGalleryItem{
		{Source: "live", Station: "r1", Start: time.Now().UTC(), Kind: "image", Mode: "sstv", Name: "a.png", Verification: "sstv reception verified: test reason"},
		{Source: "live", Station: "r1", Start: time.Now().UTC().Add(-time.Hour), Kind: "image", Mode: "sstv", Name: "b.png"},
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
	if len(items) != 2 {
		t.Fatalf("%d items, want 2", len(items))
	}
	byMode := map[bool]monitor.Activity{}
	for _, a := range items {
		byMode[a.Historical] = a
	}
	if !byMode[false].Historical && byMode[false].Verification == "" {
		t.Error("verified gallery entry lost its reason")
	}
	if !byMode[true].Historical {
		t.Error("legacy gallery entry not marked historical")
	}
}
