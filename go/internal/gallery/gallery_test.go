package gallery

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/decode"
	"github.com/chaim12345/sdrstream/go/internal/satnogs"
)

func TestClassify(t *testing.T) {
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 16)...)
	wav := append([]byte("RIFF\x24\x00\x00\x00WAVEfmt "), make([]byte, 16)...)
	cases := []struct {
		name string
		data []byte
		kind string
		ext  string
	}{
		{"png", png, "image", ".png"},
		{"jpeg", []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10}, "image", ".jpg"},
		{"gif", []byte("GIF89a......"), "image", ".gif"},
		{"wav", wav, "audio", ".wav"},
		{"ogg", []byte("OggS\x00\x02\x00\x00"), "audio", ".ogg"},
		{"text", []byte("hello from a cube sat\nsecond line\n"), "text", ".txt"},
		{"binary", []byte{0x00, 0x01, 0xff, 0x02, 0x00, 0x7e}, "binary", ".bin"},
	}
	for _, tc := range cases {
		kind, ext := Classify(tc.data)
		if kind != tc.kind || ext != tc.ext {
			t.Errorf("%s: got %s/%s, want %s/%s", tc.name, kind, ext, tc.kind, tc.ext)
		}
	}
}

func TestCollect(t *testing.T) {
	observations := []satnogs.Observation{{
		ID:           42,
		SatName:      "ISS",
		NoradID:      25544,
		Station:      "test-station",
		Start:        time.Unix(1_700_000_000, 0),
		Mode:         "SSTV",
		FrequencyHz:  145_800_000,
		WaterfallURL: "https://example.test/waterfall.png",
		PayloadURL:   "https://example.test/audio.ogg",
		DemodURL:     "https://example.test/demod-1",
		Demoddata: []satnogs.Demoddata{
			{PayloadDemod: "https://example.test/demod-1"},
			{PayloadDemod: "https://example.test/demod-2"},
		},
	}}
	items := Collect(observations)
	if len(items) != 4 {
		t.Fatalf("got %d items, want 4 (waterfall, audio, two demod files)", len(items))
	}
	if items[0].Kind != "image" || items[1].Kind != "audio" {
		t.Fatalf("waterfall/audio kinds = %q/%q", items[0].Kind, items[1].Kind)
	}
	if items[0].ObservationID != 42 || items[0].Satellite != "ISS" {
		t.Fatalf("metadata not copied: %+v", items[0])
	}
	if !strings.HasPrefix(items[0].Name, "42-") {
		t.Fatalf("name %q not prefixed with the observation id", items[0].Name)
	}
}

func TestFetch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/image":
			_, _ = w.Write(append([]byte("\x89PNG\r\n\x1a\n"), []byte("pixels")...))
		case "/text":
			_, _ = w.Write([]byte("CQ DE RS44\nframe 001 ok\n"))
		case "/raw":
			_, _ = w.Write([]byte{0x00, 0xff, 0x7e, 0x00, 0x01})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	items := []Item{
		{ObservationID: 7, Name: "7-image", URL: server.URL + "/image"},
		{ObservationID: 7, Name: "7-text", URL: server.URL + "/text"},
		{ObservationID: 7, Name: "7-raw", URL: server.URL + "/raw"},
	}
	dir := t.TempDir()
	saved, err := Fetch(items, dir, 5*time.Second, 1<<20, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved) != 2 {
		t.Fatalf("saved %d items, want 2 (image and text, binary dropped)", len(saved))
	}
	if saved[0].Kind != "image" || filepath.Ext(saved[0].Path) != ".png" {
		t.Fatalf("image item: %+v", saved[0])
	}
	if _, err := os.Stat(saved[0].Path); err != nil {
		t.Fatalf("image not written: %v", err)
	}
	if saved[1].Kind != "text" || !strings.Contains(saved[1].Text, "frame 001") {
		t.Fatalf("text item: %+v", saved[1])
	}
	if _, err := os.Stat(filepath.Join(dir, "7-raw.bin")); !os.IsNotExist(err) {
		t.Fatalf("binary artefact was kept even though keepBinary is false")
	}
}

func TestCollectSourceAndLicenceLink(t *testing.T) {
	observations := []satnogs.Observation{{ID: 15259232, SatName: "ISS", Station: "Veve-UHF", PayloadURL: "https://example.test/audio.ogg"}}
	items := Collect(observations)
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	if items[0].Source != "satnogs" {
		t.Fatalf("source = %q, want satnogs", items[0].Source)
	}
	want := "https://network.satnogs.org/observations/15259232/"
	if items[0].ObservationURL != want {
		t.Fatalf("observation url = %q, want %q", items[0].ObservationURL, want)
	}
	if LicenseName != "CC BY-SA 4.0" || !strings.Contains(Attribution, "SatNOGS") {
		t.Fatalf("licence constants changed: %q %q", LicenseName, Attribution)
	}
}

func TestWriteManifestRoundtrip(t *testing.T) {
	dir := t.TempDir()
	items := []Item{{
		Source: "satnogs", ObservationURL: ObservationLink(42), ObservationID: 42,
		Satellite: "ISS", Station: "test-station", Kind: "image", Name: "42-w.png",
		Path: filepath.Join(dir, "42-w.png"), Size: 10,
	}}
	if err := WriteManifest(items, dir); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var loaded []Item
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 {
		t.Fatalf("loaded %d items, want 1", len(loaded))
	}
	if loaded[0].Path != "" {
		t.Fatalf("manifest must strip absolute paths, got %q", loaded[0].Path)
	}
	if loaded[0].Source != "satnogs" || loaded[0].ObservationURL != ObservationLink(42) {
		t.Fatalf("provenance lost: %+v", loaded[0])
	}
}

func TestDecodeFramesFromSSTVAudio(t *testing.T) {
	fixture := filepath.Join("..", "decode", "testdata", "sstv_robot36.wav")
	if _, err := os.Stat(fixture); err != nil {
		t.Skip("sstv fixture not present:", err)
	}
	dir := t.TempDir()
	name := "15259232-audio.wav"
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
	audio := Item{
		Source: "satnogs", ObservationURL: ObservationLink(15259232), ObservationID: 15259232,
		Satellite: "ISS", NoradID: 25544, Station: "test-station",
		Start: time.Unix(1_700_000_000, 0), Mode: "SSTV", FrequencyHz: 437_550_000,
		Kind: "audio", Name: name, Path: filepath.Join(dir, name),
	}
	other := Item{Kind: "audio", Mode: "APRS", Name: "x.wav", Path: audio.Path}
	frames := DecodeFrames([]Item{audio, other}, dir)
	if len(frames) != 1 {
		t.Fatalf("got %d frames, want 1 from the SSTV audio only", len(frames))
	}
	frame := frames[0]
	if frame.Kind != "image" || frame.DecodedFrom != name {
		t.Fatalf("frame lineage wrong: %+v", frame)
	}
	if frame.Source != "satnogs" || frame.ObservationURL != ObservationLink(15259232) || frame.Station != "test-station" {
		t.Fatalf("provenance not carried to the decoded frame: %+v", frame)
	}
	png, err := os.ReadFile(frame.Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(png) < 8 || !bytes.Equal(png[:8], []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatalf("decoded artefact is not a PNG (%d bytes)", len(png))
	}
}

func TestDecodeFramesSkipsUnverifiableAPT(t *testing.T) {
	// SSTV audio labelled APT must not produce a picture: the APT gate
	// rejects it, and the artefact is left untouched.
	fixture := filepath.Join("..", "decode", "testdata", "sstv_robot36.wav")
	if _, err := os.Stat(fixture); err != nil {
		t.Skip("sstv fixture not present:", err)
	}
	dir := t.TempDir()
	name := "9-noaa-apt.wav"
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
	audio := Item{Kind: "audio", Mode: "APT", Name: name, Path: filepath.Join(dir, name), Source: "satnogs"}
	if frames := DecodeFrames([]Item{audio}, dir); len(frames) != 0 {
		t.Fatalf("unverifiable APT audio produced %d frame(s), want 0", len(frames))
	}
}

func TestDecodeFramesWithoutFFmpegSkipsOGG(t *testing.T) {
	// No ffmpeg (the Android case): compressed payloads are skipped
	// quietly and the call returns promptly instead of failing or hanging.
	t.Setenv("PATH", t.TempDir())
	if decode.FFmpegAvailable() {
		t.Fatal("ffmpeg unexpectedly available with an empty PATH")
	}
	dir := t.TempDir()
	name := "1-audio.ogg"
	if err := os.WriteFile(filepath.Join(dir, name), []byte("OggS fake"), 0o644); err != nil {
		t.Fatal(err)
	}
	audio := Item{Kind: "audio", Mode: "SSTV", Name: name, Path: filepath.Join(dir, name), Source: "satnogs"}
	start := time.Now()
	if frames := DecodeFrames([]Item{audio}, dir); len(frames) != 0 {
		t.Fatalf("compressed audio without ffmpeg produced %d frame(s), want 0", len(frames))
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("ffmpeg-less skip took %v, should be immediate", elapsed)
	}
}
