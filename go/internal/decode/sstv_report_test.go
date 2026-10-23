package decode

import (
	"os"
	"testing"

	"github.com/chaim12345/sdrstream/go/internal/verify"
)

// SSTV decode evidence: VIS + parity prove a header was heard, not
// frame geometry. Geometry evidence is the sync-lock count: coasted
// lines kept their expected position under a constant-clock
// assumption. These tests pin the measured separation between the
// good corpus and a constructed geometry-failure fixture:
//
//	good (clean + ~15 dB SNR impaired, all 6 modes): coastFrac 0.000
//	sstv_martin_m1_syncloss_drift:                  coastFrac 0.316
//
// The negative fixture (tools/gen_sstv_syncloss_fixture.py, synthetic,
// no off-air material) erases the 1200 Hz sync pulses on lines 80-179
// of a clean Martin M1 frame and stretches the band 1.012x, so the
// true line clock drifts while the decoder coasts. Measured on the
// committed WAV: a contiguous 80-line coast run (lines 89-168) plus
// an isolated coast at 176; lines after the run re-anchor. VIS is
// intact and all 256 lines sample in-bounds, so the frame passes
// every pre-gate decode check - decoded=256/256, no error - while its
// mid-frame geometry is torn. That is the publication hole the coast
// gate exists to catch.
//
// Fixture-derived evidence, not physical truth: it proves the
// discriminator separates this constructed failure from the good
// corpus, not that every real bad frame trips it.

func TestSSTVReportGoodCorpusNoCoast(t *testing.T) {
	for _, name := range []string{"sstv_martin_m1", "sstv_martin_m2", "sstv_scottie_s1",
		"sstv_scottie_s2", "sstv_scottie_dx", "sstv_robot36",
		"sstv_martin_m2_impaired", "sstv_robot36_impaired"} {
		samples, rate, err := ReadWAV("testdata/" + name + ".wav")
		if err != nil {
			t.Fatalf("ReadWAV %s: %v", name, err)
		}
		_, rep, err := SSTVImageDetailed(samples, rate)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if rep.CoastedLines != 0 {
			t.Errorf("%s: coasted=%d, want 0 - good frames lock every sync", name, rep.CoastedLines)
		}
		if rep.Mode == "" || rep.Height == 0 {
			t.Errorf("%s: incomplete report %+v", name, rep)
		}
		// Publication gate, all three families: bounded coasting
		// evidence must pass.
		pngBytes, _, err := SSTVImageDetailed(samples, rate)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if v := verify.SSTVImage("sstv", pngBytes, &rep); !v.Live {
			t.Errorf("%s: good frame failed publication gate: %s", name, v.Reason)
		}
	}
}

func TestSSTVReportSynclossDriftNegative(t *testing.T) {
	samples, rate, err := ReadWAV("testdata/sstv_martin_m1_syncloss_drift.wav")
	if err != nil {
		t.Fatalf("ReadWAV: %v", err)
	}
	pngBytes, rep, err := SSTVImageDetailed(samples, rate)
	// The hole, documented: a torn-geometry frame still decodes.
	if err != nil {
		t.Fatalf("negative fixture must still decode (the gate, not the decoder, flags it): %v", err)
	}
	if rep.DecodedLines != rep.Height {
		t.Errorf("decoded=%d/%d, want full - line-presence floor alone cannot catch this", rep.DecodedLines, rep.Height)
	}
	if rep.CoastFraction() < 0.2 || rep.CoastFraction() > 0.5 {
		t.Errorf("coastFrac=%.3f, want ~0.32 (80-line coast run); fixture changed?", rep.CoastFraction())
	}
	if len(pngBytes) == 0 {
		t.Error("no PNG produced")
	}
	// The gate catches what the decoder alone does not.
	if v := verify.SSTVImage("sstv", pngBytes, &rep); v.Live {
		t.Error("torn-geometry frame passed the publication gate")
	}
	if os.Getenv("SSTV_WRITE_DEBUG_PNG") != "" {
		os.WriteFile("/tmp/sstv_negative.png", pngBytes, 0o644)
	}
}
