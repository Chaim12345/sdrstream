package rdio

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// wavBytes returns a minimal valid-length WAV payload; the uploader only
// checks that there is more than a header, so a literal body is enough.
func wavBytes() []byte {
	return append([]byte("RIFF\x00\x00\x00\x00WAVEfmt "), make([]byte, 64)...)
}

func TestUploadSendsRdioCallFields(t *testing.T) {
	audioPath := filepath.Join(t.TempDir(), "activity-001-119800000-speech.wav")
	if err := os.WriteFile(audioPath, wavBytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	var got struct {
		key, system, talkgroup, group, tag, frequency, timestamp, name, mime string
		audio                                                                []byte
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/call-upload" {
			t.Errorf("path = %q, want /api/call-upload", r.URL.Path)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("ParseMultipartForm: %v", err)
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		got.key = r.FormValue("key")
		got.system = r.FormValue("systemLabel")
		got.talkgroup = r.FormValue("talkgroupLabel")
		got.group = r.FormValue("talkgroupGroup")
		got.tag = r.FormValue("talkgroupTag")
		got.frequency = r.FormValue("frequency")
		got.timestamp = r.FormValue("timestamp")
		got.name = r.FormValue("audioName")
		got.mime = r.FormValue("audioMime")
		file, _, err := r.FormFile("audio")
		if err != nil {
			t.Errorf("FormFile(audio): %v", err)
		} else {
			got.audio, _ = io.ReadAll(file)
		}
		fmt.Fprint(w, "Call imported successfully.\n")
	}))
	defer server.Close()

	started := time.Date(2026, 10, 22, 15, 4, 5, 0, time.UTC)
	err := Upload(context.Background(), Options{
		BaseURL: server.URL, APIKey: "secret-key", SystemLabel: "sdr-ladar",
	}, Call{
		AudioPath: audioPath, Timestamp: started, FrequencyHz: 119800000,
		TalkgroupLabel: "119.8000 MHz AM", TalkgroupGroup: "voice",
		TalkgroupTag: "tower, cleared to land",
	})
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if got.key != "secret-key" {
		t.Errorf("key = %q", got.key)
	}
	if got.system != "sdr-ladar" {
		t.Errorf("systemLabel = %q", got.system)
	}
	if got.talkgroup != "119.8000 MHz AM" {
		t.Errorf("talkgroupLabel = %q", got.talkgroup)
	}
	if got.group != "voice" || got.tag != "tower, cleared to land" {
		t.Errorf("group/tag = %q/%q", got.group, got.tag)
	}
	if got.frequency != "119800000" {
		t.Errorf("frequency = %q", got.frequency)
	}
	if got.timestamp != strconv.FormatInt(started.UnixMilli(), 10) {
		t.Errorf("timestamp = %q", got.timestamp)
	}
	if got.name != "activity-001-119800000-speech.wav" || got.mime != "audio/x-wav" {
		t.Errorf("audioName/audioMime = %q/%q", got.name, got.mime)
	}
	if string(got.audio) != string(wavBytes()) {
		t.Errorf("audio payload differs (%d bytes)", len(got.audio))
	}
}

func TestUploadRejectsBadKey(t *testing.T) {
	audioPath := filepath.Join(t.TempDir(), "call.wav")
	if err := os.WriteFile(audioPath, wavBytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Invalid API key for system 1 talkgroup 2.", http.StatusUnauthorized)
	}))
	defer server.Close()
	err := Upload(context.Background(), Options{BaseURL: server.URL, APIKey: "wrong"}, Call{
		AudioPath: audioPath, Timestamp: time.Now(),
	})
	if err == nil || !strings.Contains(err.Error(), "Invalid API key") {
		t.Fatalf("err = %v, want the server's rejection", err)
	}
}

func TestQueueUploadsAndCounts(t *testing.T) {
	audioPath := filepath.Join(t.TempDir(), "call.wav")
	if err := os.WriteFile(audioPath, wavBytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	received := make(chan struct{}, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- struct{}{}
		fmt.Fprint(w, "Call imported successfully.\n")
	}))
	defer server.Close()
	queue := NewQueue(Options{BaseURL: server.URL, APIKey: "k"}, nil)
	for i := 0; i < 3; i++ {
		queue.Enqueue(Call{AudioPath: audioPath, Timestamp: time.Now(), TalkgroupLabel: "test"})
	}
	queue.Close()
	uploaded, failed, err := queue.Stats()
	if uploaded != 3 || failed != 0 || err != nil {
		t.Fatalf("stats = %d/%d/%v", uploaded, failed, err)
	}
	if len(received) != 3 {
		t.Fatalf("server received %d calls, want 3", len(received))
	}
}
