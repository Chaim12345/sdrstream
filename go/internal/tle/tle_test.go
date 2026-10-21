package tle

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// gpSample is a General-Perturbations record as CelesTrak serves it: the
// numeric CCSDS fields arrive quoted as JSON strings.
const gpSample = `[
  {
    "OBJECT_NAME": "ISS (ZARYA)",
    "NORAD_CAT_ID": "25544",
    "EPOCH": "2026-10-20T15:51:33.979680",
    "MEAN_MOTION": 15.48959761,
    "ECCENTRICITY": 0.000675,
    "INCLINATION": 51.6326,
    "RA_OF_ASC_NODE": 39.1774,
    "ARG_OF_PERICENTER": 287.932,
    "MEAN_ANOMALY": 72.0932,
    "BSTAR": 0.00016489,
    "MEAN_MOTION_DOT": 8.635e-05,
    "MEAN_MOTION_DDOT": 0.0,
    "CLASSIFICATION_TYPE": "U",
    "EPHEMERIS_TYPE": "0",
    "ELEMENT_SET_NO": "999",
    "REV_AT_EPOCH": "46830"
  }
]`

func withTestCache(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old := DefaultCacheDir
	DefaultCacheDir = dir
	t.Cleanup(func() { DefaultCacheDir = old })
	return dir
}

func TestNormalizeGPJSONUnquotesNumericFields(t *testing.T) {
	normalized, err := NormalizeGPJSON(gpSample)
	if err != nil {
		t.Fatalf("normalizeGPJSON: %v", err)
	}
	for _, want := range []string{
		`"NORAD_CAT_ID":25544`,
		`"ELEMENT_SET_NO":999`,
		`"REV_AT_EPOCH":46830`,
		`"EPHEMERIS_TYPE":0`,
	} {
		if !strings.Contains(normalized, want) {
			t.Errorf("normalized payload missing %s\n%s", want, normalized)
		}
	}
	// Name and classification stay strings.
	for _, want := range []string{`"OBJECT_NAME":"ISS (ZARYA)"`, `"CLASSIFICATION_TYPE":"U"`} {
		if !strings.Contains(normalized, want) {
			t.Errorf("normalized payload missing %s", want)
		}
	}
}

func TestNormalizeGPJSONLeavesNonNumbersAlone(t *testing.T) {
	payload := `[{"NORAD_CAT_ID":"not-a-number","OBJECT_NAME":"X"}]`
	normalized, err := NormalizeGPJSON(payload)
	if err != nil {
		t.Fatalf("normalizeGPJSON: %v", err)
	}
	if !strings.Contains(normalized, `"NORAD_CAT_ID":"not-a-number"`) {
		t.Errorf("non-numeric value should be left as-is: %s", normalized)
	}
}

func TestFetchGPRejectsNonJSON(t *testing.T) {
	withTestCache(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "<html>maintenance</html>")
	}))
	defer server.Close()
	old := GPGroups["amateur"]
	GPGroups["amateur"] = server.URL + "/gp"
	t.Cleanup(func() { GPGroups["amateur"] = old })

	if _, err := FetchGP("amateur", time.Second); err == nil {
		t.Fatal("FetchGP accepted a non-JSON body")
	}
}

func TestLoadGPDownloadsThenServesFromCache(t *testing.T) {
	cacheDir := withTestCache(t)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, gpSample)
	}))
	defer server.Close()
	old := GPGroups["amateur"]
	GPGroups["amateur"] = server.URL + "/gp"
	t.Cleanup(func() { GPGroups["amateur"] = old })

	text, err := LoadGP("amateur", 24, false, time.Second)
	if err != nil {
		t.Fatalf("LoadGP: %v", err)
	}
	if !strings.Contains(text, `"NORAD_CAT_ID":25544`) {
		t.Errorf("cached text was not normalized: %s", text)
	}
	if _, err := os.Stat(filepath.Join(cacheDir, "gp_amateur.json")); err != nil {
		t.Fatalf("GP cache not written: %v", err)
	}

	// A fresh read within the age limit must not hit the server again.
	requests = 0
	cached, err := LoadGP("amateur", 24, false, time.Second)
	if err != nil {
		t.Fatalf("LoadGP (cached): %v", err)
	}
	if requests != 0 {
		t.Errorf("cache was not used, %d requests", requests)
	}
	if cached != text {
		t.Error("cached GP text differs from the fetched copy")
	}

	// Offline mode works while a cache exists...
	if _, err := LoadGP("amateur", 24, true, time.Second); err != nil {
		t.Errorf("offline LoadGP with a cache: %v", err)
	}
}

func TestLoadGPOfflineWithoutCacheFails(t *testing.T) {
	withTestCache(t)
	_, err := LoadGP("amateur", 24, true, time.Second)
	if err == nil {
		t.Fatal("offline LoadGP without a cache should fail")
	}
}

func TestGPAgeSecondsMissingGroup(t *testing.T) {
	withTestCache(t)
	if age := GPAgeSeconds("amateur"); age != -1 {
		t.Errorf("GPAgeSeconds without a cache = %v, want -1", age)
	}
}
