package decode

// Standing decoder-fixture corpus: every top-level wav in testdata is
// pinned by sha256 in testdata/manifest.json together with provenance and
// the decode facts the current decoders produce for it. The gates below
// fail on fixture drift, on an unpinned wav, or on a decode outcome that
// no longer matches the pinned facts. Facts were measured at manifest
// creation; per project rule a failing gate is REPORTED, never fixed by
// tuning the gate.
//
// Tool modes (never in CI):
//   SDRSTREAM_MEASURE_FIXTURES=1  print measured facts instead of asserting
//   SDRSTREAM_REGEN_FIXTURES=1    regenerate the synthetic WEFAX wavs

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chaim12345/sdrstream/go/internal/testutil/wefaxfixture"
)

type decoderFixture struct {
	Name        string  `json:"name"`
	Kind        string  `json:"kind"` // sstv | wefax
	SHA256      string  `json:"sha256"`
	Provenance  string  `json:"provenance"`
	ExpectError string  `json:"expect_error,omitempty"` // decode currently fails with this substring
	Width       int     `json:"width,omitempty"`
	Height      int     `json:"height,omitempty"`
	SyncSource  string  `json:"sync_source,omitempty"`
	DriftPPM    float64 `json:"drift_ppm,omitempty"`
	DriftTolPPM float64 `json:"drift_tol_ppm,omitempty"`
}

type decoderManifest struct {
	Version int    `json:"version"`
	Purpose string `json:"purpose"`
	// WEFAXRegen documents the exact synthetic parameters behind the wefax
	// fixtures so TestGenDecoderWEFAXFixtures reproduces them byte-for-byte.
	WEFAXRegen map[string]wefaxfixture.Fixture `json:"wefax_regen"`
	Fixtures   []decoderFixture                `json:"fixtures"`
}

func loadDecoderManifest(t *testing.T) *decoderManifest {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "manifest.json"))
	if err != nil {
		t.Fatalf("read decoder manifest: %v", err)
	}
	var m decoderManifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse decoder manifest: %v", err)
	}
	if m.Version != 1 {
		t.Fatalf("manifest version = %d, want 1", m.Version)
	}
	return &m
}

func fixtureSHA256(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// TestDecoderFixtureHashes pins the corpus: every manifest entry must hash
// exactly, and every top-level testdata wav must be in the manifest.
func TestDecoderFixtureHashes(t *testing.T) {
	m := loadDecoderManifest(t)
	seen := map[string]bool{}
	for _, f := range m.Fixtures {
		seen[f.Name+".wav"] = true
		if got := fixtureSHA256(t, filepath.Join("testdata", f.Name+".wav")); got != f.SHA256 {
			t.Errorf("%s.wav sha256 = %.16s, manifest pins %.16s (fixture drift or corruption)", f.Name, got, f.SHA256)
		}
	}
	wavs, err := filepath.Glob(filepath.Join("testdata", "*.wav"))
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range wavs {
		if !seen[filepath.Base(w)] {
			t.Errorf("%s is not in testdata/manifest.json - pin it or remove it", filepath.Base(w))
		}
	}
}

// measureFixture decodes one fixture and returns the facts the gate pins.
func measureFixture(t *testing.T, f decoderFixture) (facts decoderFixture, err error) {
	t.Helper()
	samples, rate, err := ReadWAV(filepath.Join("testdata", f.Name+".wav"))
	if err != nil {
		return facts, err
	}
	facts = decoderFixture{Name: f.Name, Kind: f.Kind}
	switch f.Kind {
	case "sstv":
		pngBytes, err := SSTVImage(samples, rate)
		if err != nil {
			facts.ExpectError = err.Error()
			return facts, nil
		}
		img, err := png.Decode(bytes.NewReader(pngBytes))
		if err != nil {
			return facts, fmt.Errorf("png decode: %w", err)
		}
		facts.Width, facts.Height = img.Bounds().Dx(), img.Bounds().Dy()
	case "wefax":
		pngBytes, rep, err := WEFAXImageDetailed(samples, rate, 120, 576)
		if err != nil {
			facts.ExpectError = err.Error()
			return facts, nil
		}
		img, err := png.Decode(bytes.NewReader(pngBytes))
		if err != nil {
			return facts, fmt.Errorf("png decode: %w", err)
		}
		facts.Width, facts.Height = img.Bounds().Dx(), img.Bounds().Dy()
		facts.SyncSource = rep.SyncSource
		facts.DriftPPM = rep.DriftPPM
	default:
		return facts, fmt.Errorf("unknown kind %q", f.Kind)
	}
	return facts, nil
}

// TestDecoderFixtureGates decodes every manifest fixture and compares the
// outcome with the pinned facts (dimensions and sync source exact, drift
// within its declared tolerance, expected errors matched by substring).
// Set SDRSTREAM_MEASURE_FIXTURES=1 to print the facts instead.
func TestDecoderFixtureGates(t *testing.T) {
	m := loadDecoderManifest(t)
	measure := os.Getenv("SDRSTREAM_MEASURE_FIXTURES") == "1"
	for _, f := range m.Fixtures {
		t.Run(f.Name, func(t *testing.T) {
			facts, err := measureFixture(t, f)
			if err != nil {
				t.Fatalf("measure: %v", err)
			}
			if measure {
				t.Logf("MEASURED %s kind=%s sha256=%s err=%q %dx%d sync=%q drift=%.0fppm",
					facts.Name, facts.Kind, fixtureSHA256(t, filepath.Join("testdata", facts.Name+".wav"))[:16],
					facts.ExpectError, facts.Width, facts.Height, facts.SyncSource, facts.DriftPPM)
				return
			}
			if f.ExpectError != "" {
				if !strings.Contains(facts.ExpectError, f.ExpectError) {
					t.Fatalf("decode error = %q, manifest pins %q (behavior change - report it)", facts.ExpectError, f.ExpectError)
				}
				return
			}
			if facts.ExpectError != "" {
				t.Fatalf("decode failed: %s (manifest pins success %dx%d - behavior change, report it)", facts.ExpectError, f.Width, f.Height)
			}
			if facts.Width != f.Width || facts.Height != f.Height {
				t.Errorf("image = %dx%d, manifest pins %dx%d", facts.Width, facts.Height, f.Width, f.Height)
			}
			if f.Kind == "wefax" {
				if facts.SyncSource != f.SyncSource {
					t.Errorf("sync source = %q, manifest pins %q", facts.SyncSource, f.SyncSource)
				}
				if d := facts.DriftPPM - f.DriftPPM; d > f.DriftTolPPM || d < -f.DriftTolPPM {
					t.Errorf("drift = %.0f ppm, manifest pins %.0f +/- %.0f", facts.DriftPPM, f.DriftPPM, f.DriftTolPPM)
				}
			}
		})
	}
}

// TestGenDecoderWEFAXFixtures regenerates the synthetic WEFAX fixtures with
// the exact parameters in the manifest (SDRSTREAM_REGEN_FIXTURES=1 only).
// The wavs are byte-stable because wefaxfixture.Encode is deterministic.
func TestGenDecoderWEFAXFixtures(t *testing.T) {
	if os.Getenv("SDRSTREAM_REGEN_FIXTURES") != "1" {
		t.Skip("set SDRSTREAM_REGEN_FIXTURES=1 to regenerate the WEFAX fixture wavs")
	}
	m := loadDecoderManifest(t)
	for name, f := range m.WEFAXRegen {
		samples := wefaxfixture.Encode(f)
		pcm := make([]int16, len(samples))
		for i, v := range samples {
			if v > 1 {
				v = 1
			}
			if v < -1 {
				v = -1
			}
			pcm[i] = int16(v * 32767)
		}
		path := filepath.Join("testdata", name+".wav")
		if err := os.WriteFile(path, WriteWAV16(pcm, int(f.Rate)), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s sha256=%s", path, fixtureSHA256(t, path))
	}
}
