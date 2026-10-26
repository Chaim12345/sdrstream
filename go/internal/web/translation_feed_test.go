package web

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/monitor"
	"github.com/chaim12345/sdrstream/go/internal/translate"
)

// A recorded translation rides the activity feed next to its original row,
// matched by the feed's own dedupe key, with the original untouched.
func TestActivityFeedCarriesTranslation(t *testing.T) {
	s := &Server{}
	start := time.Unix(1_700_000_000, 0)
	s.liveGallery = []LiveGalleryItem{{
		Source: "live", Verification: "gate passed", Start: start,
		Mode: "speech", Kind: "voice", FrequencyHz: 3855000,
		Text: "texto en espanol", Name: "live-20231114T181320Z-1.txt",
	}}
	s.RecordTranslation(monitor.Activity{
		Kind: "voice", Mode: "speech", Detail: "texto en espanol",
		Started: start, FrequencyHz: 3855000,
	}, translate.Result{
		Original: "texto en espanol", Translated: "text in Spanish",
		Status: translate.StatusTranslated, TargetLang: "en", Machine: true,
		Provider: "fake", Model: "t0", SourceLang: "es",
	})

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/activity.json", nil))
	var rows []struct {
		Detail      string `json:"detail"`
		Translation *struct {
			Translated string `json:"translated"`
			Status     string `json:"status"`
			Machine    bool   `json:"machine_translated"`
			Provider   string `json:"provider"`
		} `json:"translation"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&rows); err != nil {
		t.Fatalf("decode feed: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("no activity rows")
	}
	found := false
	for _, r := range rows {
		if r.Detail == "texto en espanol" {
			found = true
			if r.Translation == nil {
				t.Fatal("original row carries no translation")
			}
			if r.Translation.Translated != "text in Spanish" || r.Translation.Status != "translated" || !r.Translation.Machine {
				t.Fatalf("bad translation payload: %+v", r.Translation)
			}
		}
	}
	if !found {
		t.Fatal("original voice row missing from feed")
	}
}

// Rows without a recorded translation carry no translation field.
func TestActivityFeedNoTranslationNoField(t *testing.T) {
	s := &Server{}
	s.liveGallery = []LiveGalleryItem{{
		Source: "live", Verification: "gate passed", Start: time.Unix(1_700_000_000, 0),
		Mode: "speech", Kind: "voice", FrequencyHz: 7053000,
		Text: "plain english net", Name: "live-20231114T181320Z-2.txt",
	}}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/activity.json", nil))
	var rows []struct {
		Detail      string          `json:"detail"`
		Translation json.RawMessage `json:"translation"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&rows); err != nil {
		t.Fatalf("decode feed: %v", err)
	}
	for _, r := range rows {
		if len(r.Translation) > 0 && string(r.Translation) != "null" {
			t.Fatalf("unexpected translation on %q", r.Detail)
		}
	}
}

func TestTranslationIdentityBoundaries(t *testing.T) {
	s := &Server{}
	start := time.Unix(1700000000, 0)
	a := monitor.Activity{ID: 1, Started: start, FrequencyHz: 3855000, Kind: "voice", Mode: "speech", Detail: "texte original"}
	r := translate.Result{Original: a.Detail, Status: translate.StatusTranslated, Translated: "original text", TargetLang: "en", Machine: true}
	s.RecordTranslation(a, r)
	key := activityDedupeKey(a.Started.UnixNano(), int64(a.FrequencyHz), "voice", a.Mode, a.Detail)
	if len(s.translations) != 1 || s.translations[key].Original != a.Detail {
		t.Fatal("translation missing")
	}
	for _, other := range []monitor.Activity{
		{Started: start.Add(time.Nanosecond), FrequencyHz: a.FrequencyHz, Kind: a.Kind, Mode: a.Mode, Detail: a.Detail},
		{Started: start, FrequencyHz: a.FrequencyHz + 1, Kind: a.Kind, Mode: a.Mode, Detail: a.Detail},
		{Started: start, FrequencyHz: a.FrequencyHz, Kind: a.Kind, Mode: "other", Detail: a.Detail},
		{Started: start, FrequencyHz: a.FrequencyHz, Kind: a.Kind, Mode: a.Mode, Detail: "different text"},
	} {
		k := activityDedupeKey(other.Started.UnixNano(), int64(other.FrequencyHz), "voice", other.Mode, other.Detail)
		if _, ok := s.translations[k]; ok {
			t.Fatalf("unrelated activity matched %+v", other)
		}
	}
	// Gallery reassigns IDs; identity deliberately does not depend on ID.
	twin := a
	twin.ID = 999
	s.RecordTranslation(twin, r)
	if len(s.translations) != 1 {
		t.Fatal("gallery twin duplicated")
	}
	wrong := r
	wrong.Original = "another transcript"
	wrong.Translated = "incorrect attachment"
	s.RecordTranslation(a, wrong)
	if s.translations[key].Translated != r.Translated {
		t.Fatal("mismatched original overwrote result")
	}
}
