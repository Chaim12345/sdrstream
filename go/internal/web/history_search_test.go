package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/gallery"
	"github.com/chaim12345/sdrstream/go/internal/monitor"
	"github.com/chaim12345/sdrstream/go/internal/voice"
)

func historyRequest(t *testing.T, s *Server, query string) historyPage {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/history/search"+query, nil))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var page historyPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Items == nil {
		t.Fatal("items must serialize as [], not null")
	}
	return page
}

func TestHistorySourceClassesMatchGalleryEnum(t *testing.T) {
	cases := []struct{ source, verdict, from, want string }{
		{"test-fixture", "pass", "", historySynthetic}, {"test-fixture", "pass", "a.wav", historySynthetic},
		{"satnogs", "pass", "", historyForeign}, {"other", "pass", "a.wav", historyForeign},
		{"", "pass", "", historyUnknown}, {"", "pass", "a.wav", historyUnknown}, {"", "", "", historyUnknown},
		{"live", "pass", "", historyLiveVerified}, {"live", "pass", "a.wav", historyLiveRedecode},
		{"live", "", "", historyLiveUnverified}, {"live", "", "a.wav", historyLiveUnverified},
	}
	for _, c := range cases {
		t.Run(c.source+":"+c.verdict+":"+c.from, func(t *testing.T) {
			it := LiveGalleryItem{Source: c.source, Verification: c.verdict, DecodedFrom: c.from}
			if got := historyGalleryClass(it); got != c.want {
				t.Fatalf("got %q want %q", got, c.want)
			}
		})
	}
	if got := historyCaptureClass(Capture{Source: "live", Verified: true, GateVerdict: "failed"}); got != historyLiveUnverified {
		t.Fatal(got)
	}
}

func TestHistorySearchMergesStoresAndKeepsProvenance(t *testing.T) {
	s := newTestServer(t)
	now := time.Now().UTC().Truncate(time.Second)
	s.captures = []Capture{{Path: "a.wav", URL: "/audio/a.wav", Mode: "rtty", Decode: "CQ mixed CASE", Source: "test-fixture", Verified: true, Start: now},
		{Path: "legacy.wav", Mode: "rtty", Decode: "CQ unknown", Verified: true, Start: now},
		{Path: "foreign.wav", Mode: "cw", Decode: "FOREIGN", Source: "elsewhere", Start: now}}
	s.liveGallery = []LiveGalleryItem{
		{Name: "known.txt", Text: "CQ live", Mode: "rtty", Source: "live", Verification: "recorded pass", Start: now},
		{Name: "unknown.txt", Text: "CQ legacy decode", Mode: "rtty", Verification: "decoder passed", DecodedFrom: "legacy.wav", Start: now},
		{Name: "redecode.txt", Text: "CQ redecode", Mode: "rtty", Source: "live", Verification: "decoder passed", DecodedFrom: "a.wav", Start: now},
		{Name: "synthetic.txt", Text: "CQ synthetic", Mode: "rtty", Source: "test-fixture", Verification: "decoder passed", Start: now},
	}
	s.gallery = []gallery.Item{{Name: "sat.txt", Text: "CQ imported", Mode: "rtty", ObservationID: 42, Start: now}}
	m := &monitor.Monitor{}
	m.AddActivity(monitor.Activity{Started: now, Mode: "rtty", Detail: "CQ receiver url", Source: "https://receiver.test", Verification: "recorded pass"})
	s.monitor = m
	page := historyRequest(t, s, "?q=cQ&mode=RTTY")
	if page.Total != 8 || page.Available != 9 {
		t.Fatalf("total=%d available=%d", page.Total, page.Available)
	}
	byID := map[string]HistoryResult{}
	for _, it := range page.Items {
		byID[it.ID] = it
	}
	if byID["satnogs:42:sat.txt"].SourceClass != historyForeign || byID["satnogs:42:sat.txt"].Source != "satnogs" {
		t.Fatal("SatNOGS legacy backfill missing")
	}
	if byID["decode:unknown.txt"].SourceClass != historyUnknown || !byID["decode:unknown.txt"].Redecode {
		t.Fatal("unknown re-decode promoted or hidden")
	}
	for _, it := range page.Items {
		if it.Store == "activity" && (it.SourceClass != historyLiveVerified || it.Source != "live" || it.Receiver != "https://receiver.test") {
			t.Fatal(it)
		}
	}
	for _, c := range []string{historySynthetic, historyForeign, historyUnknown, historyLiveVerified, historyLiveRedecode} {
		p := historyRequest(t, s, "?source-class="+c)
		if p.Total == 0 {
			t.Fatal("empty class", c)
		}
		for _, it := range p.Items {
			if it.SourceClass != c {
				t.Fatal(it)
			}
		}
	}
	p := historyRequest(t, s, "?redecode=true")
	if p.Total != 2 {
		t.Fatalf("redecode total %d", p.Total)
	}
	p = historyRequest(t, s, "?redecode=true&source-class=unknown")
	if p.Total != 1 || p.Items[0].ID != "decode:unknown.txt" {
		t.Fatal(p)
	}
	p = historyRequest(t, s, "?redecode=false")
	if p.Total != 7 {
		t.Fatal(p.Total)
	}
}

func TestHistoryPaginationFiltersBeforePagingAndStableTies(t *testing.T) {
	s := newTestServer(t)
	for i := 0; i < 123; i++ {
		s.captures = append(s.captures, Capture{Path: fmt.Sprintf("%03d.wav", i), Mode: "cw", Decode: "needle", Start: time.Unix(1, 0)})
	}
	first := historyRequest(t, s, "?q=needle")
	if len(first.Items) != 50 || first.Total != 123 || first.NextOffset == nil || *first.NextOffset != 50 {
		t.Fatal(first)
	}
	second := historyRequest(t, s, "?q=needle&offset=50")
	last := historyRequest(t, s, "?q=needle&offset=100")
	if len(second.Items) != 50 || len(last.Items) != 23 || last.NextOffset != nil {
		t.Fatal("bad page lengths")
	}
	seen := map[string]bool{}
	prev := ""
	for _, p := range []historyPage{first, second, last} {
		for _, it := range p.Items {
			if seen[it.ID] || it.ID < prev {
				t.Fatal("duplicate or unstable tie", it.ID)
			}
			seen[it.ID] = true
			prev = it.ID
		}
	}
	for _, query := range []string{"?q=absent", "?offset=9223372036854775807", "?source-class=synthetic"} {
		p := historyRequest(t, s, query)
		if len(p.Items) != 0 || p.NextOffset != nil {
			t.Fatal(p)
		}
	}
}

func TestHistorySearchSidecarsAndLocalLinks(t *testing.T) {
	s := newTestServer(t)
	s.capturesDir = t.TempDir()
	if err := voice.WriteWAV(filepath.Join(s.capturesDir, "side.wav"), make([]float64, 100), 12000); err != nil {
		t.Fatal(err)
	}
	if err := WriteCaptureMetadata(s.capturesDir, "side.wav", CaptureMetadata{Source: "test-fixture", Mode: "cw", Detail: "Sidecar needle", Started: time.Now()}); err != nil {
		t.Fatal(err)
	}
	s.captures = append(s.captures, Capture{Path: "side.wav", Decode: "duplicate stale memory"})
	if err := os.WriteFile(filepath.Join(s.capturesDir, "orphan.txt"), []byte("mode=cw\nsource=test-fixture\ndecode=Orphan needle\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p := historyRequest(t, s, "?q=needle")
	if p.Total != 2 {
		t.Fatalf("sidecar/duplicate handling: %+v", p)
	}
	for _, it := range p.Items {
		if strings.Contains(it.Name, s.capturesDir) || strings.Contains(it.ID, s.capturesDir) {
			t.Fatal("private path leaked")
		}
		if it.ID == "capture:side.wav" && it.URL != "/audio/side.wav" {
			t.Fatal(it.URL)
		}
		if strings.HasPrefix(it.Name, "orphan") && it.URL != "" {
			t.Fatal("orphan must not offer missing audio", it.URL)
		}
	}
	for _, name := range []string{"../secret", "a/b.wav", `a\b.wav`, ".", "..", ""} {
		if historyLocalURL("/audio/", name) != "" {
			t.Fatal(name)
		}
	}
}

func TestHistorySearchRequestValidation(t *testing.T) {
	s := newTestServer(t)
	for _, query := range []string{"?limit=0", "?limit=201", "?limit=-1", "?offset=-1", "?offset=abc", "?source-class=live", "?redecode=yes", "?q=" + strings.Repeat("x", 1025)} {
		r := httptest.NewRecorder()
		s.Handler().ServeHTTP(r, httptest.NewRequest("GET", "/api/history/search"+query, nil))
		if r.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d", query, r.Code)
		}
	}
	r := httptest.NewRecorder()
	s.Handler().ServeHTTP(r, httptest.NewRequest("POST", "/api/history/search", nil))
	if r.Code != 405 || r.Header().Get("Allow") != "GET" {
		t.Fatal(r.Code)
	}
}

func TestHistorySearchSnapshotConcurrentUpdates(t *testing.T) {
	s := newTestServer(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			s.mu.Lock()
			s.liveGallery = append(s.liveGallery, LiveGalleryItem{Name: fmt.Sprintf("%d.txt", i), Source: "test-fixture", Text: "synthetic"})
			s.mu.Unlock()
		}
	}()
	for i := 0; i < 30; i++ {
		historyRequest(t, s, "?source-class=synthetic&limit=5")
	}
	<-done
}

func TestActivitySerializesConfirmedSourceClassesWithoutNewRows(t *testing.T) {
	s := newTestServer(t)
	now := time.Now().UTC()
	m := &monitor.Monitor{}
	s.monitor = m
	passed := m.AddActivity(monitor.Activity{Started: now, Mode: "cw", Kind: "cw", Detail: "PASS", Source: "receiver"})
	rejected := m.AddActivity(monitor.Activity{Started: now.Add(-time.Second), Mode: "cw", Kind: "cw", Detail: "FAIL", Source: "receiver"})
	m.AddActivity(monitor.Activity{Started: now.Add(-2 * time.Second), Mode: "cw", Kind: "cw", Detail: "UNGATED", Source: "receiver"})
	m.RecordGate(passed, true, "monitor pass")
	m.RecordGate(rejected, false, "specific rejection")
	s.liveGallery = []LiveGalleryItem{
		{Name: "twin.txt", Start: now, Mode: "cw", Kind: "text", Text: "PASS", Source: "live", Verification: "gallery pass"},
		{Name: "old.txt", Start: now.Add(-3 * time.Second), Mode: "cw", Text: "LEGACY"},
		{Name: "synthetic.txt", Source: "test-fixture", Verification: "not live", Text: "FIXTURE"},
		{Name: "foreign.txt", Source: "satnogs", Verification: "not live", Text: "FOREIGN"},
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/activity.json", nil))
	var rows []struct {
		monitor.Activity
		SourceClass string `json:"source_class"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatal("included foreign/fixture or duplicated twin", len(rows))
	}
	for _, r := range rows {
		switch r.Detail {
		case "PASS":
			if r.SourceClass != historyLiveVerified || r.Verification != "gallery pass" {
				t.Fatal(r)
			}
		case "FAIL":
			if r.SourceClass != historyLiveUnverified || r.Rejected != "specific rejection" {
				t.Fatal(r)
			}
		case "UNGATED":
			if r.SourceClass != historyLiveUnverified {
				t.Fatal(r)
			}
		case "LEGACY":
			if r.SourceClass != historyUnknown || !r.Historical {
				t.Fatal(r)
			}
		default:
			t.Fatal(r)
		}
	}
}

func TestShellWiresHistorySearch(t *testing.T) {
	page, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Index(string(page), `<script src="/static/history.js"></script>`) < strings.Index(string(page), `<script src="/static/app.js"></script>`) {
		t.Fatal("history must load after app")
	}
	for _, marker := range []string{`id="history-form"`, `id="history-results"`, `data-view="history"`} {
		if !strings.Contains(string(page), marker) {
			t.Fatal(marker)
		}
	}
	sw, err := staticFS.ReadFile("static/sw.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sw), "'/static/history.js'") || !strings.Contains(string(sw), "sdrstream-shell-v4") {
		t.Fatal("history shell cache not updated")
	}
	if _, err := staticFS.ReadFile("static/history.js"); err != nil {
		t.Fatal(err)
	}
}

func TestHistorySourceAliasAndUnknownParameterRejection(t *testing.T) {
	s := newTestServer(t)
	s.captures = []Capture{{Path: "fixture.wav", Source: "test-fixture"}, {Path: "own.wav", Source: "live", Verified: true}}
	for _, query := range []string{"?source_class=synthetic", "?source-class=synthetic", "?source_class=synthetic&source-class=synthetic"} {
		p := historyRequest(t, s, query)
		if p.Total != 1 || p.Items[0].SourceClass != historySynthetic {
			t.Fatal(query, p)
		}
	}
	for _, query := range []string{"?source_clas=synthetic", "?unknown=synthetic", "?source_class=synthetic&source-class=foreign", "?q=a&q=b", "?source_class=synthetic&source_class=foreign"} {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/history/search"+query, nil))
		if rec.Code != 400 {
			t.Fatalf("%s status%d", query, rec.Code)
		}
	}
}
