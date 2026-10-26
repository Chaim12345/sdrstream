package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/chaim12345/sdrstream/go/internal/decode"
	"github.com/chaim12345/sdrstream/go/internal/verify"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: wefaxharness <capture.wav> <out.png>")
		os.Exit(2)
	}
	wavPath, outPath := os.Args[1], os.Args[2]
	keep := len(os.Args) > 3 && os.Args[3] == "-keep"
	samples, rate, err := decode.ReadWAV(wavPath)
	if err != nil {
		fmt.Println(emitJSON(map[string]any{"ok": false, "stage": "readwav", "error": err.Error()}))
		os.Exit(1)
	}
	// Mirror the app's own capture decode path exactly:
	// go/internal/web/capture_decode.go case "wefax".
	pngData, rep, derr := decode.WEFAXImageDetailed(samples, rate, 120, 576)
	if derr != nil || len(pngData) == 0 {
		fmt.Println(emitJSON(map[string]any{"ok": false, "stage": "decode", "error": fmt.Sprint(derr), "rate": rate, "samples": len(samples)}))
		os.Exit(1)
	}
	v := verify.WEFAXImage("wefax", pngData, rep.SyncSource, rep.WedgeContrast, rep.DriftPPM, rep.DriftMinPPM, rep.DriftMaxPPM, rep.PeriodScore, rep.DeskewCorr)
	out := map[string]any{
		"ok": v.Live, "reason": v.Reason, "rate": rate, "samples": len(samples),
		"duration_sec": float64(len(samples)) / float64(rate),
		"blackHz": rep.BlackHz, "whiteHz": rep.WhiteHz,
		"pairConfidence": rep.PairConfidence, "antiCorrelation": rep.AntiCorrelation,
		"wedgeContrast": rep.WedgeContrast, "syncSource": rep.SyncSource,
		"periodScore": rep.PeriodScore, "phasingStartSec": rep.PhasingStartSec,
		"phasingLines": rep.PhasingLines, "driftPPM": rep.DriftPPM,
		"driftMinPPM": rep.DriftMinPPM, "driftMaxPPM": rep.DriftMaxPPM,
		"startToneSec": rep.StartToneSec, "startToneEndSec": rep.StartToneEndSec,
		"png_bytes": len(pngData),
	}
	if v.Live || keep {
		if werr := os.WriteFile(outPath, pngData, 0o644); werr != nil {
			out["ok"] = false
			out["stage"] = "write"
			out["error"] = werr.Error()
			fmt.Println(emitJSON(out))
			os.Exit(1)
		}
		out["png"] = outPath
		if !v.Live {
			out["png_gate_rejected"] = true
		}
	}
	fmt.Println(emitJSON(out))
	if !v.Live {
		os.Exit(1)
	}
}

func emitJSON(v map[string]any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}
