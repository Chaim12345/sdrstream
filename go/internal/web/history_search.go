package web

import (
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// HistoryResult is a searchable record, not a fresh-reception claim. Each
// store keeps its own identity; a capture and its decoded artefact remain
// separate results rather than being guessed to be the same observation.
type HistoryResult struct {
	ID           string    `json:"id"`
	Store        string    `json:"store"`
	Name         string    `json:"name,omitempty"`
	Started      time.Time `json:"started"`
	Mode         string    `json:"mode,omitempty"`
	Kind         string    `json:"kind,omitempty"`
	Text         string    `json:"text,omitempty"`
	Receiver     string    `json:"receiver,omitempty"`
	FrequencyHz  float64   `json:"frequency_hz,omitempty"`
	Source       string    `json:"source,omitempty"`
	SourceClass  string    `json:"source_class"`
	Verification string    `json:"verification,omitempty"`
	Rejected     string    `json:"rejected,omitempty"`
	Historical   bool      `json:"historical,omitempty"`
	DecodedFrom  string    `json:"decoded_from,omitempty"`
	Redecode     bool      `json:"redecode"`
	GateVerdict  string    `json:"gate_verdict,omitempty"`
	URL          string    `json:"url,omitempty"`
}

type historyPage struct {
	Items      []HistoryResult `json:"items"`
	Total      int             `json:"total"`
	Available  int             `json:"available"`
	Limit      int             `json:"limit"`
	Offset     int             `json:"offset"`
	NextOffset *int            `json:"next_offset"`
	Scope      string          `json:"scope"`
}

const (
	historySynthetic      = "synthetic"
	historyForeign        = "foreign"
	historyUnknown        = "unknown"
	historyLiveVerified   = "live-verified"
	historyLiveUnverified = "live-unverified"
	historyLiveRedecode   = "live-redecode"
)

// historyGalleryClass maps the existing source-owned enum, retaining unknown
// provenance even when a legacy re-decode has a measured decoder verdict.
func historyGalleryClass(it LiveGalleryItem) string {
	if it.Source == "test-fixture" {
		return historySynthetic
	}
	if it.Source == "" {
		return historyUnknown
	}
	switch it.galleryLiveClass() {
	case galleryLiveVerified:
		return historyLiveVerified
	case galleryLiveRedecode:
		return historyLiveRedecode
	case galleryLiveHistorical:
		return historyLiveUnverified
	default:
		return historyForeign
	}
}

func historyCaptureClass(c Capture) string {
	verdict := ""
	// An explicit failure wins over the legacy summary flag.
	if c.GateVerdict == "passed" || (c.GateVerdict == "" && c.Verified) {
		verdict = "recorded pass"
	}
	return historyGalleryClass(LiveGalleryItem{Source: c.Source, Verification: verdict})
}

func historyLocalURL(prefix, name string) string {
	if name == "" || filepath.Base(name) != name || strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		return ""
	}
	return prefix + url.PathEscape(name)
}

func (s *Server) historyRecords() []HistoryResult {
	captures := s.scanCaptures()
	s.mu.Lock()
	captures = append(captures, s.captures...)
	live := append([]LiveGalleryItem(nil), s.liveGallery...)
	sat := make([]HistoryResult, 0, len(s.gallery))
	// Snapshot only public metadata. Filesystem paths never enter results.
	for _, it := range s.gallery {
		source := it.Source
		if source == "" {
			source = "satnogs"
		} // this store is the SatNOGS import store
		sat = append(sat, HistoryResult{
			ID:    "satnogs:" + strconv.Itoa(it.ObservationID) + ":" + it.Name,
			Store: "gallery", Name: it.Name, Started: it.Start, Mode: it.Mode, Kind: it.Kind,
			Text: it.Text, Receiver: it.Station, FrequencyHz: float64(it.FrequencyHz),
			Source: source, SourceClass: historyForeign, DecodedFrom: it.DecodedFrom, Redecode: it.DecodedFrom != "",
			URL: historyLocalURL("/gallery/", it.Name),
		})
	}
	instance := s.monitor
	s.mu.Unlock()
	out := make([]HistoryResult, 0, len(captures)+len(live)+len(sat))
	seenCaptures := map[string]bool{}
	for _, c := range captures {
		// scanCaptures and in-memory captures may describe the same named WAV.
		// Prefer the sidecar-backed snapshot, which is appended first.
		if c.Path != "" && seenCaptures[c.Path] {
			continue
		}
		seenCaptures[c.Path] = true
		verdict := ""
		if c.GateVerdict == "passed" || (c.GateVerdict == "" && c.Verified) {
			verdict = c.Gate
		}
		rejected := ""
		if c.GateVerdict == "failed" {
			rejected = c.Gate
		}
		audioURL := ""
		if c.URL != "" {
			audioURL = historyLocalURL("/audio/", c.Path)
		}
		out = append(out, HistoryResult{
			ID: "capture:" + c.Path, Store: "capture", Name: filepath.Base(c.Path), Started: c.Start,
			Mode: c.Mode, Kind: c.Kind, Text: c.Decode, Receiver: c.Receiver, FrequencyHz: c.FrequencyHz,
			Source: c.Source, SourceClass: historyCaptureClass(c),
			Verification: verdict, Rejected: rejected, GateVerdict: c.GateVerdict, URL: audioURL,
		})
	}
	for _, it := range live {
		out = append(out, HistoryResult{
			ID: "decode:" + it.Name, Store: "decode", Name: it.Name, Started: it.Start, Mode: it.Mode, Kind: it.Kind,
			Text: it.Text, Receiver: it.Station, FrequencyHz: float64(it.FrequencyHz), Source: it.Source,
			SourceClass:  historyGalleryClass(it),
			Verification: it.Verification, Historical: it.galleryLiveClass() != galleryLiveVerified,
			DecodedFrom: it.DecodedFrom, Redecode: it.DecodedFrom != "", URL: historyLocalURL("/gallery/", it.Name),
		})
	}
	for _, it := range sat {
		out = append(out, it)
	}
	if instance != nil {
		for _, a := range instance.Activities() {
			if a.Detail == "" {
				continue
			}
			// Activity.Source names the receiver, not provenance. Do not promote it.
			out = append(out, HistoryResult{ID: fmt.Sprintf("activity:%d:%d", a.ID, a.Started.UnixNano()), Store: "activity",
				Started: a.Started, Mode: a.Mode, Kind: a.Kind, Text: a.Detail, Receiver: a.Source, FrequencyHz: a.FrequencyHz,
				SourceClass: historyUnknown, Verification: a.Verification, Rejected: a.Rejected, Historical: a.Historical})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Started.Equal(out[j].Started) {
			return out[i].ID < out[j].ID
		}
		return out[i].Started.After(out[j].Started)
	})
	return out
}

func (s *Server) handleHistorySearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	values := r.URL.Query()
	q := strings.TrimSpace(values.Get("q"))
	if len(q) > 1024 {
		http.Error(w, "q is limited to 1024 bytes", http.StatusBadRequest)
		return
	}
	class := values.Get("source-class")
	switch class {
	case "", historySynthetic, historyForeign, historyUnknown, historyLiveVerified, historyLiveUnverified, historyLiveRedecode:
	default:
		http.Error(w, "unknown source-class", http.StatusBadRequest)
		return
	}
	redecode := values.Get("redecode")
	if redecode != "" && redecode != "true" && redecode != "false" {
		http.Error(w, "redecode must be true or false", http.StatusBadRequest)
		return
	}
	limit, offset := 50, 0
	for key, dest := range map[string]*int{"limit": &limit, "offset": &offset} {
		if raw := values.Get(key); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 0 || (key == "limit" && (n == 0 || n > 200)) {
				http.Error(w, "invalid "+key, http.StatusBadRequest)
				return
			}
			*dest = n
		}
	}
	records := s.historyRecords()
	matches := make([]HistoryResult, 0)
	needle := strings.ToLower(q)
	mode := strings.ToLower(strings.TrimSpace(values.Get("mode")))
	for _, it := range records {
		if redecode != "" && it.Redecode != (redecode == "true") {
			continue
		}
		if class != "" && it.SourceClass != class {
			continue
		}
		if mode != "" && strings.ToLower(it.Mode) != mode {
			continue
		}
		haystack := strings.ToLower(strings.Join([]string{it.Name, it.Text, it.Mode, it.Kind, it.Receiver, it.Source, strconv.FormatFloat(it.FrequencyHz, 'f', -1, 64)}, " "))
		if needle != "" && !strings.Contains(haystack, needle) {
			continue
		}
		matches = append(matches, it)
	}
	total := len(matches)
	if offset > total {
		offset = total
	}
	end := total
	if total-offset > limit {
		end = offset + limit
	} // avoid addition overflow from user input
	var next *int
	if end < total {
		n := end
		next = &n
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, historyPage{Items: matches[offset:end], Total: total, Available: len(records), Limit: limit, Offset: offset, NextOffset: next,
		Scope: "Current metadata snapshot only; archive listing capped at 5000 captures. Pages may change as stores update; records across stores are not observation counts."})
}
