package gallery

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chaim12345/sdrstream/go/internal/decode"
	"time"

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
	items := []Item{audio}
	if frames := DecodeFrames(items, dir); len(frames) != 0 {
		t.Fatalf("compressed audio without ffmpeg produced %d frame(s), want 0", len(frames))
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("ffmpeg-less skip took %v, should be immediate", elapsed)
	}
	if !strings.Contains(items[0].DecodeStatus, "ffmpeg not installed") {
		t.Fatalf("skip reason not reported on the artefact: %q", items[0].DecodeStatus)
	}
}

func TestDecodeFramesRejectsOversizedAudio(t *testing.T) {
	fixture := filepath.Join("..", "decode", "testdata", "sstv_robot36.wav")
	if _, err := os.Stat(fixture); err != nil {
		t.Skip("sstv fixture not present:", err)
	}
	dir := t.TempDir()
	name := "5-long.wav"
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
	// Shrink the duration bound so the 36 s fixture exceeds it.
	old := maxDecodeDurationSec
	maxDecodeDurationSec = 10
	t.Cleanup(func() { maxDecodeDurationSec = old })
	audio := Item{Kind: "audio", Mode: "SSTV", Name: name, Path: filepath.Join(dir, name), Source: "satnogs"}
	items := []Item{audio}
	if frames := DecodeFrames(items, dir); len(frames) != 0 {
		t.Fatalf("over-limit audio produced %d frame(s), want 0", len(frames))
	}
	if !strings.Contains(items[0].DecodeStatus, "decode limit") {
		t.Fatalf("duration limit reason missing: %q", items[0].DecodeStatus)
	}
}

func TestDecodeFramesSkipsUnlabelledModes(t *testing.T) {
	dir := t.TempDir()
	name := "6-aprs.wav"
	if err := os.WriteFile(filepath.Join(dir, name), []byte("not a wav"), 0o644); err != nil {
		t.Fatal(err)
	}
	audio := Item{Kind: "audio", Mode: "AFSK1200", Name: name, Path: filepath.Join(dir, name), Source: "satnogs"}
	items := []Item{audio}
	if frames := DecodeFrames(items, dir); len(frames) != 0 {
		t.Fatalf("non-image mode produced %d frame(s), want 0", len(frames))
	}
	if items[0].DecodeStatus != "" {
		t.Fatalf("non-image mode should not be attempted, got status %q", items[0].DecodeStatus)
	}
}

// wavFixture builds a minimal PCM WAV: fmt chunk (mono, rate, 16-bit) and
// a data chunk whose DECLARED size can differ from the payload written.
func wavFixture(declaredData uint32, payloadBytes int, rate uint32) []byte {
	var b bytes.Buffer
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, uint32(36+declaredData))
	b.WriteString("WAVE")
	b.WriteString("fmt ")
	_ = binary.Write(&b, binary.LittleEndian, uint32(16))
	_ = binary.Write(&b, binary.LittleEndian, uint16(1))  // PCM
	_ = binary.Write(&b, binary.LittleEndian, uint16(1))  // mono
	_ = binary.Write(&b, binary.LittleEndian, rate)       // sample rate
	_ = binary.Write(&b, binary.LittleEndian, rate*2)     // byte rate
	_ = binary.Write(&b, binary.LittleEndian, uint16(2))  // block align
	_ = binary.Write(&b, binary.LittleEndian, uint16(16)) // bits
	b.WriteString("data")
	_ = binary.Write(&b, binary.LittleEndian, declaredData)
	b.Write(make([]byte, payloadBytes))
	return b.Bytes()
}

func TestProbeWAVDurationClampsToActualPayload(t *testing.T) {
	dir := t.TempDir()
	// Header declares 80000 data bytes (5s at 8kHz mono 16-bit) but the
	// file only holds 8000 (0.5s): the actual payload sets the duration.
	p := filepath.Join(dir, "padded.wav")
	if err := os.WriteFile(p, wavFixture(80000, 8000, 8000), 0o644); err != nil {
		t.Fatal(err)
	}
	d, _, err := probeWAV(p)
	if err != nil {
		t.Fatal(err)
	}
	if d < 0.49 || d > 0.51 {
		t.Fatalf("duration = %v, want ~0.5 from the actual payload, not 5 from the declaration", d)
	}
	// A truthful header still measures correctly.
	p2 := filepath.Join(dir, "honest.wav")
	if err := os.WriteFile(p2, wavFixture(80000, 80000, 8000), 0o644); err != nil {
		t.Fatal(err)
	}
	d, _, err = probeWAV(p2)
	if err != nil {
		t.Fatal(err)
	}
	if d < 4.9 || d > 5.1 {
		t.Fatalf("duration = %v, want ~5", d)
	}
}

func TestBoundedConversionCapsOutputDuringWrite(t *testing.T) {
	if !decode.FFmpegAvailable() {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	ogg := filepath.Join(dir, "tone.ogg")
	mk := exec.Command(decode.FFmpegBinary(""), "-y", "-v", "error",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=3:sample_rate=8000", ogg)
	if out, err := mk.CombinedOutput(); err != nil {
		t.Skipf("cannot make ogg fixture: %v: %s", err, out)
	}
	dst := filepath.Join(dir, "out.wav")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	// 4KB cap: ffmpeg stops writing packets once it is crossed, so the
	// file lands within a small flush/trailer slop rather than at the
	// uncapped size. The production post-check tolerates this via slack.
	if err := runFFmpeg(ctx, decode.FFmpegBinary(""), "-y", "-v", "error", "-i", ogg, "-ac", "1", "-fs", "4096", dst); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	full := filepath.Join(dir, "full.wav")
	if err := runFFmpeg(ctx, decode.FFmpegBinary(""), "-y", "-v", "error", "-i", ogg, "-ac", "1", full); err != nil {
		t.Fatal(err)
	}
	fiFull, err := os.Stat(full)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() > 4096+16384 {
		t.Fatalf("capped conversion wrote %d bytes, far over the 4KB cap", fi.Size())
	}
	if fi.Size()*2 >= fiFull.Size() {
		t.Fatalf("cap did not bound the output: capped %d, uncapped %d", fi.Size(), fiFull.Size())
	}
}

func TestDecodeFramesOversizedCompressedSkipped(t *testing.T) {
	if !decode.FFmpegAvailable() {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	ogg := filepath.Join(dir, "7-tone.ogg")
	mk := exec.Command(decode.FFmpegBinary(""), "-y", "-v", "error",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=3:sample_rate=8000", ogg)
	if out, err := mk.CombinedOutput(); err != nil {
		t.Skipf("cannot make ogg fixture: %v: %s", err, out)
	}
	old := maxWAVBytes
	maxWAVBytes = 1024 // 3s of 8kHz mono PCM is ~48KB: over this cap
	t.Cleanup(func() { maxWAVBytes = old })
	audio := Item{Kind: "audio", Mode: "SSTV", Name: "7-tone.ogg", Path: ogg, Source: "satnogs"}
	items := []Item{audio}
	if frames := DecodeFrames(items, dir); len(frames) != 0 {
		t.Fatalf("over-cap compressed audio produced %d frame(s), want 0", len(frames))
	}
	if !strings.Contains(items[0].DecodeStatus, "over the") {
		t.Fatalf("over-limit reason missing: %q", items[0].DecodeStatus)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "*.wav")); len(left) != 0 {
		t.Fatalf("over-cap conversion left WAV files behind: %v", left)
	}
}

// craftWAVRaw builds a WAV file from explicit chunk specs so tests can
// mismatch declared vs actual sizes. Chunks are (id, declaredSize, payload).
func craftWAVRaw(t *testing.T, chunks [][3]interface{}) string {
	t.Helper()
	body := []byte("RIFF\x00\x00\x00\x00WAVE")
	for _, c := range chunks {
		id := c[0].(string)
		declared := uint32(c[1].(int))
		payload := c[2].([]byte)
		body = append(body, id[0], id[1], id[2], id[3])
		var sz [4]byte
		binary.LittleEndian.PutUint32(sz[:], declared)
		body = append(body, sz[:]...)
		body = append(body, payload...)
		if len(payload)%2 == 1 {
			body = append(body, 0)
		}
	}
	p := filepath.Join(t.TempDir(), "crafted.wav")
	if err := os.WriteFile(p, body, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func fmtChunk(rate, channels int) []byte {
	b := make([]byte, 16)
	binary.LittleEndian.PutUint16(b[0:2], 1)
	binary.LittleEndian.PutUint16(b[2:4], uint16(channels))
	binary.LittleEndian.PutUint32(b[4:8], uint32(rate))
	binary.LittleEndian.PutUint16(b[12:14], uint16(channels*2))
	binary.LittleEndian.PutUint16(b[14:16], 16)
	return b
}

func TestProbeMatchesDecoder_UnderstatedDeclared(t *testing.T) {
	// Declared 0.5s but 5s of payload present: BOTH probe and decoder must
	// measure only the declared 0.5s (decoder clamps end to declared size).
	half := 44100 // bytes for 0.5s mono 16-bit @ 44100
	full := make([]byte, half*10)
	p := craftWAVRaw(t, [][3]interface{}{
		{"fmt ", 16, fmtChunk(44100, 1)},
		{"data", half, full},
	})
	d, _, err := probeWAV(p)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if d != 0.5 {
		t.Fatalf("probe %.4fs, want 0.5s", d)
	}
	samples, rate, err := decode.ReadWAV(p)
	if err != nil {
		t.Fatalf("ReadWAV: %v", err)
	}
	if rate != 44100 || len(samples) != 22050 {
		t.Fatalf("decoder read %d samples @ %d, want 22050 @ 44100 (declared portion only)", len(samples), rate)
	}
}

func TestProbeMatchesDecoder_MultipleDataChunks(t *testing.T) {
	// First data chunk 0.5s, second data chunk claims 1GB: decoder reads the
	// FIRST chunk only; probe must agree.
	half := 44100
	p := craftWAVRaw(t, [][3]interface{}{
		{"fmt ", 16, fmtChunk(44100, 1)},
		{"data", half, make([]byte, half)},
		{"data", 1 << 30, make([]byte, half)},
	})
	d, _, err := probeWAV(p)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if d != 0.5 {
		t.Fatalf("probe %.4fs, want 0.5s (first data chunk only)", d)
	}
	samples, _, err := decode.ReadWAV(p)
	if err != nil {
		t.Fatalf("ReadWAV: %v", err)
	}
	if len(samples) != 22050 {
		t.Fatalf("decoder read %d samples, want 22050 (first chunk only)", len(samples))
	}
}

func TestProbeMatchesDecoder_EmptyFirstDataChunk(t *testing.T) {
	// Empty first data chunk, real audio in the second: decoder reads the
	// empty first chunk; probe must not be deflated by the second.
	p := craftWAVRaw(t, [][3]interface{}{
		{"fmt ", 16, fmtChunk(44100, 1)},
		{"data", 0, []byte{}},
		{"data", 441000, make([]byte, 441000)},
	})
	d, _, err := probeWAV(p)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if d != 0 {
		t.Fatalf("probe %.4fs, want 0 (first data chunk is empty)", d)
	}
	samples, _, err := decode.ReadWAV(p)
	if err != nil {
		t.Fatalf("ReadWAV: %v", err)
	}
	if len(samples) != 0 {
		t.Fatalf("decoder read %d samples, want 0", len(samples))
	}
}

func TestProbeMatchesDecoder_GarbageBlockAlign(t *testing.T) {
	// Decoder ignores block-align/byte-rate fields; probe derives bytes/sec
	// from rate*channels*bits. Garbage block align must not change either.
	fc := fmtChunk(44100, 1)
	binary.LittleEndian.PutUint32(fc[8:12], 999999999) // byte rate garbage
	binary.LittleEndian.PutUint16(fc[12:14], 77)       // block align garbage
	p := craftWAVRaw(t, [][3]interface{}{
		{"fmt ", 16, fc},
		{"data", 88200, make([]byte, 88200)},
	})
	d, _, err := probeWAV(p)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if d != 1.0 {
		t.Fatalf("probe %.4fs, want 1.0s", d)
	}
	samples, rate, err := decode.ReadWAV(p)
	if err != nil {
		t.Fatalf("ReadWAV: %v", err)
	}
	if len(samples) != 44100 || rate != 44100 {
		t.Fatalf("decoder read %d samples @ %d, want 44100 @ 44100", len(samples), rate)
	}
}

func TestDecodeFramesSampleBudgetPreflight(t *testing.T) {
	// 40 MB mono 44.1 kHz WAV: 475 s, UNDER the 480 s duration cap, but
	// 20,971,520 estimated samples, OVER the 16 M sample budget. The budget
	// check must be the one that fires, before the decoder allocates.
	dir := t.TempDir()
	name := "7-budget.wav"
	payload := 40 * 1024 * 1024
	if err := os.WriteFile(filepath.Join(dir, name), wavFixture(uint32(payload), payload, 44100), 0o644); err != nil {
		t.Fatal(err)
	}
	audio := Item{Kind: "audio", Mode: "SSTV", Name: name, Path: filepath.Join(dir, name), Source: "satnogs"}
	items := []Item{audio}
	if frames := DecodeFrames(items, dir); len(frames) != 0 {
		t.Fatalf("over-budget audio produced %d frame(s), want 0", len(frames))
	}
	if !strings.Contains(items[0].DecodeStatus, "memory budget") {
		t.Fatalf("budget reason missing (duration cap must not fire first): %q", items[0].DecodeStatus)
	}
	if items[0].Path == "" || items[0].Name != name {
		t.Fatalf("source item not preserved: %+v", items[0])
	}
}

func TestProbeWAVEstimatedSamples(t *testing.T) {
	// estSamples mirrors decoder arithmetic: dataBytes/2/channels, rate
	// does not reduce it.
	dir := t.TempDir()
	p := filepath.Join(dir, "s.wav")
	if err := os.WriteFile(p, wavFixture(176400, 176400, 44100), 0o644); err != nil {
		t.Fatal(err)
	}
	d, n, err := probeWAV(p)
	if err != nil {
		t.Fatal(err)
	}
	if d < 1.99 || d > 2.01 || n != 88200 {
		t.Fatalf("probe = %vs / %d samples, want 2s / 88200", d, n)
	}
	samples, rate, err := decode.ReadWAV(p)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(samples)) != n || rate != 44100 {
		t.Fatalf("decoder read %d samples, probe estimated %d", len(samples), n)
	}
}
