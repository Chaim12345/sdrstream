package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// PreviewStartupCheck: a preview without a UI password must fail at boot,
// before anything is served; the error must never carry the password.
func TestPreviewStartupCheck(t *testing.T) {
	if err := PreviewStartupCheck(true, ""); err == nil {
		t.Fatal("preview without password must refuse startup")
	} else if strings.Contains(err.Error(), "password123") {
		t.Fatal("startup error must never echo secrets")
	}
	if err := PreviewStartupCheck(true, "password123"); err != nil {
		t.Fatalf("preview with password should pass: %v", err)
	}
	if err := PreviewStartupCheck(false, ""); err != nil {
		t.Fatalf("non-preview without password stays allowed: %v", err)
	}
}

func previewServer() *Server {
	s := New("", "", WithPreviewMode(true))
	return s
}

// Preview mode denies receiver start even with SDRSTREAM_PREVIEW_NO_START
// unset: the guard must not depend on the accidental environment.
func TestPreviewModeDeniesMonitorStartWithoutEnv(t *testing.T) {
	s := previewServer()
	t.Cleanup(func() { s.cancel() })
	req := httptest.NewRequest("POST", "http://example.test/api/monitor/control", strings.NewReader("action=start&kind=openwebrx&server=x"))
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.handleMonitorControl(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("preview monitor start: got %d, want 403", rec.Code)
	}
}

// Tuning retunes a live receiver: remote control, denied in preview.
func TestPreviewModeDeniesTune(t *testing.T) {
	s := previewServer()
	t.Cleanup(func() { s.cancel() })
	req := httptest.NewRequest("POST", "http://example.test/api/tune.json", strings.NewReader("frequency_hz=145800000&mode=usb"))
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.handleTune(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("preview tune: got %d, want 403", rec.Code)
	}
}

// Scan control: unpause/concurrency makes the server contact receivers -
// denied; pausing only quiets local work and stays allowed.
func TestPreviewModeScanControl(t *testing.T) {
	s := previewServer()
	t.Cleanup(func() { s.cancel() })
	mk := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "http://example.test/api/scans/control", strings.NewReader(body))
		req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		s.handleScanControl(rec, req)
		return rec
	}
	if rec := mk("paused=false"); rec.Code != http.StatusForbidden {
		t.Fatalf("preview scan unpause: got %d, want 403", rec.Code)
	}
	if rec := mk("concurrency=4"); rec.Code != http.StatusForbidden {
		t.Fatalf("preview scan concurrency: got %d, want 403", rec.Code)
	}
	if rec := mk("paused=true"); rec.Code != http.StatusOK {
		t.Fatalf("preview scan pause: got %d, want 200", rec.Code)
	}
}

// Re-decode is CPU work anyone could queue on a shared demo: denied too.
func TestPreviewModeDeniesCaptureDecode(t *testing.T) {
	s := previewServer()
	t.Cleanup(func() { s.cancel() })
	req := httptest.NewRequest("POST", "http://example.test/api/captures/decode", strings.NewReader("name=x.wav&target=navtex"))
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.handleCaptureDecode(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("preview capture decode: got %d, want 403", rec.Code)
	}
}

// Non-preview servers keep the ordinary behavior (request reaches the
// normal handler path, here failing later for lack of a running monitor).
func TestNonPreviewTuneNotGuarded(t *testing.T) {
	s := New("", "")
	t.Cleanup(func() { s.cancel() })
	req := httptest.NewRequest("POST", "http://example.test/api/tune.json", strings.NewReader("frequency_hz=145800000&mode=usb"))
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.handleTune(rec, req)
	if rec.Code == http.StatusForbidden && strings.Contains(rec.Body.String(), "preview demo") {
		t.Fatalf("non-preview tune hit the preview guard: %d %q", rec.Code, rec.Body.String())
	}
}
