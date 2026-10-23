package gallery

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// realAPRSFixture is 30 s of off-air 144.39 MHz APRS (Los Angeles, 2005)
// from Track 1 of WA8LMF's freely distributed TNC Test CD, already
// committed for the decoder tests (see internal/decode/afsk_real_test.go).
const realAPRSFixture = "../../tests/data/real_aprs_wa8lmf_30s.wav"

// AFSK-labelled audio decodes into a text artefact carrying the AX.25
// frames, with lineage and provenance preserved. The HDLC CRC gate is the
// binary ground truth for what counts as decoded.
func TestDecodeFramesAFSKRealOffAir(t *testing.T) {
	if _, err := os.Stat(realAPRSFixture); err != nil {
		t.Skip("off-air APRS fixture not present:", err)
	}
	dir := t.TempDir()
	name := "999-audio.wav"
	data, err := os.ReadFile(realAPRSFixture)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
	audio := Item{
		Source: "satnogs", ObservationURL: ObservationLink(999), ObservationID: 999,
		Satellite: "NO-44", NoradID: 26931, Station: "test-station",
		Start: time.Unix(1_700_000_000, 0), Mode: "AFSK", FrequencyHz: 145_825_000,
		Kind: "audio", Name: name, Path: filepath.Join(dir, name),
	}
	items := []Item{audio}
	frames := DecodeFrames(items, dir)
	if len(frames) != 1 {
		t.Fatalf("got %d artefacts, want 1 decoded text artefact (source status %q)", len(frames), items[0].DecodeStatus)
	}
	frame := frames[0]
	if frame.Kind != "text" || frame.DecodedFrom != name || !strings.HasSuffix(frame.Name, "-sdrstream-decoded.txt") {
		t.Fatalf("artefact lineage wrong: %+v", frame)
	}
	if frame.Source != "satnogs" || frame.ObservationID != 999 || frame.Station != "test-station" {
		t.Fatalf("provenance not carried to the decoded artefact: %+v", frame)
	}
	content, err := os.ReadFile(frame.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "KF6DQ-5") {
		t.Fatal("decoded text missing a station the reference decoder finds (KF6DQ-5)")
	}
	if items[0].DecodeStatus != "" {
		t.Fatalf("source item unexpectedly marked: %q", items[0].DecodeStatus)
	}
}

// Non-packet audio labelled AFSK produces no artefact and an explicit
// status: the CRC gate, not guesswork, decides there is nothing decoded.
func TestDecodeFramesAFSKNoValidFrames(t *testing.T) {
	fixture := filepath.Join("..", "decode", "testdata", "sstv_robot36.wav")
	if _, err := os.Stat(fixture); err != nil {
		t.Skip("sstv fixture not present:", err)
	}
	dir := t.TempDir()
	name := "998-audio.wav"
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
	items := []Item{{Kind: "audio", Mode: "APRS", Name: name, Path: filepath.Join(dir, name)}}
	if frames := DecodeFrames(items, dir); len(frames) != 0 {
		t.Fatalf("got %d artefacts from non-packet audio, want 0", len(frames))
	}
	if !strings.Contains(items[0].DecodeStatus, "no valid AX.25 frames") {
		t.Fatalf("unexpected status %q, want a no-valid-frames note", items[0].DecodeStatus)
	}
}
