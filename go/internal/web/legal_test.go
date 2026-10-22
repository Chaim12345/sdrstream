package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// The footer links to these paths; they must resolve to the real texts.
func TestLegalRoutesServed(t *testing.T) {
	server := &Server{}
	for path, want := range map[string]string{
		"/LICENSE":               "GNU AFFERO GENERAL PUBLIC LICENSE",
		"/NOTICE":                "SPDX-License-Identifier: AGPL-3.0-or-later",
		"/NOTICE-third-party.md": "Third-party notices",
	} {
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		// Unauthenticated requests may be redirected to /login when a
		// password is configured; the test server has none.
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d", path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("%s: body missing %q", path, want)
		}
	}
	for _, link := range []string{`href="/LICENSE"`, `href="/NOTICE-third-party.md"`} {
		if !strings.Contains(indexPage, link) {
			t.Fatalf("footer does not link %s", link)
		}
	}
}

func TestLegalFilesMatchRepoRoot(t *testing.T) {
	for _, name := range []string{"LICENSE", "NOTICE", "NOTICE-third-party.md"} {
		root, err := os.ReadFile("../../../" + name)
		if err != nil {
			t.Fatal(err)
		}
		embedded, err := legalFS.ReadFile("legal/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if string(root) != string(embedded) {
			t.Fatalf("go/internal/web/legal/%s differs from repo root %s; copy it over", name, name)
		}
	}
}
