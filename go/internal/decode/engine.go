package decode

// Engine selection between the built-in Go decoders and the external
// reference tools (reference.go). The decoderegress bake-off rates each Go
// decoder against its reference; the policy below keeps the Go decoder only
// where it comes within 2% of the reference on the corpus and prefers the
// reference tool otherwise. Wherever a reference binary is unavailable the
// Go decoder is always the fallback, so the binary stays useful on its own.
//
// SDRSTREAM_DECODE_ENGINE=go|ref forces an engine; unset means auto
// (reference when the policy prefers it and the binary resolves).

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
)

// Engine identifies a decode engine.
type Engine string

const (
	EngineGo  Engine = "go"
	EngineRef Engine = "ref"
)

// refPreferred lists, per decoder family, the reference tools the bake-off
// rated clearly above the Go decoder, best first. A family absent from the
// map keeps its Go decoder (within 2% of the reference or no reference).
// Regenerate from `decoderegress -bakeoff`; see go/docs/EXTERNAL-DECODERS.md.
var refPreferred = map[string][]string{
	// decoderegress bake-off, 2026-10-22 (go/docs/EXTERNAL-DECODERS.md):
	// ft8: go 78.9% vs jt9 98.5% recall; wspr: go 87.5% vs wsprd 100%.
	// pocsag/flex/aprs/acars/hfdl keep their Go decoders (>=98% of ref).
	"ft8":  {"jt9", "ft8lib"},
	"ft4":  {"jt9", "ft8lib"},
	"wspr": {"wsprd"},
}

// engineEnv is the forced engine from the environment, "" for auto.
func engineEnv() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("SDRSTREAM_DECODE_ENGINE"))) {
	case "go":
		return "go"
	case "ref", "external", "reference":
		return "ref"
	}
	return ""
}

// refFor picks the reference tool for a family under the policy and
// environment. forced=="ref" picks the strongest known tool for the family
// regardless of the policy table.
func refFor(family, forced string) RefDecoder {
	order := refPreferred[family]
	if forced == "ref" && len(order) == 0 {
		switch family {
		case "ft8", "ft4":
			order = []string{"jt9", "ft8lib"}
		case "wspr":
			order = []string{"wsprd"}
		}
	}
	if len(order) == 0 && forced != "ref" {
		return nil
	}
	for _, want := range order {
		for _, d := range RefDecoders() {
			if d.Name() == want && d.Binary() != "" {
				return d
			}
		}
	}
	return nil
}

// wavForRef writes samples to a temporary WAV for a reference tool. The
// WSJT-X tools read 12000 Hz only, so other rates are resampled first.
func wavForRef(samples []float64, rate int) (string, func(), error) {
	dir, err := os.MkdirTemp("", "sdrstream-eng-")
	if err != nil {
		return "", nil, err
	}
	if rate != 12000 {
		samples = resampleLinear(samples, rate, 12000)
		rate = 12000
	}
	p := filepath.Join(dir, "in.wav")
	if err := os.WriteFile(p, WriteWAV16(floatTo16(samples), rate), 0o644); err != nil {
		os.RemoveAll(dir)
		return "", nil, err
	}
	return p, func() { os.RemoveAll(dir) }, nil
}

// FTXDecodeAuto decodes one FT8/FT4 slot with the engine the policy picks
// and reports which engine ran ("go", or the reference tool name).
func FTXDecodeAuto(ctx context.Context, samples []float64, rate int, protocol string) ([]FTXMessage, string, error) {
	family := strings.ToLower(protocol)
	if family == "" {
		family = "ft8"
	}
	if d := refFor(family, engineEnv()); d != nil {
		p, cleanup, err := wavForRef(samples, rate)
		if err == nil {
			defer cleanup()
			if refs, err := d.DecodeWAV(ctx, p, family); err == nil {
				out := make([]FTXMessage, 0, len(refs))
				for _, r := range refs {
					out = append(out, FTXMessage{
						Protocol: strings.ToUpper(protocol), Text: r.Text,
						FreqHz: r.FreqHz, DT: r.DT, SNR: r.SNR, Engine: d.Name(),
					})
				}
				return out, d.Name(), nil
			}
		}
		// fall through to Go on any reference failure
	}
	msgs := FTXDecode(samples, rate, strings.ToUpper(protocol))
	for i := range msgs {
		msgs[i].Engine = string(EngineGo)
	}
	return msgs, string(EngineGo), nil
}

// WSPRDecodeAuto decodes one 2-minute WSPR window with the engine the policy
// picks and reports which engine ran ("go", or "wsprd").
func WSPRDecodeAuto(ctx context.Context, samples []float64, rate int) ([]WSPRMessage, string, error) {
	if d := refFor("wspr", engineEnv()); d != nil {
		p, cleanup, err := wavForRef(samples, rate)
		if err == nil {
			defer cleanup()
			if refs, err := d.DecodeWAV(ctx, p, "wspr"); err == nil {
				out := make([]WSPRMessage, 0, len(refs))
				for _, r := range refs {
					m := WSPRMessage{Text: r.Text, FreqHz: r.FreqHz, DT: r.DT, SNR: r.SNR, Engine: d.Name()}
					f := strings.Fields(r.Text)
					if len(f) >= 1 {
						m.Call = f[0]
					}
					if len(f) >= 2 {
						m.Grid = f[1]
					}
					if len(f) >= 3 {
						fmt.Sscanf(f[2], "%d", &m.DBm)
					}
					out = append(out, m)
				}
				return out, d.Name(), nil
			}
		}
	}
	msgs := WSPRDecode(samples, rate)
	for i := range msgs {
		msgs[i].Engine = string(EngineGo)
	}
	return msgs, string(EngineGo), nil
}

// engineSummary joins the engine names that produced decodes, first-seen
// order ("jt9", or "jt9+go" when a reference tool failed mid-stream and the
// Go fallback covered the rest). empty when nothing decoded.
func engineSummary(engines []string) string {
	seen := map[string]bool{}
	var out []string
	for _, e := range engines {
		if e == "" || seen[e] {
			continue
		}
		seen[e] = true
		out = append(out, e)
	}
	return strings.Join(out, "+")
}

// FTXDecodeStreamAuto decodes a longer capture by cutting it at UTC slot
// boundaries like FTXDecodeStream, then decoding each slot with the engine
// the policy picks (FTXDecodeAuto). Every returned message carries its
// SlotUnix and the Engine that decoded it; the second return value
// summarises the engines that ran.
func FTXDecodeStreamAuto(ctx context.Context, samples []float64, rate int, startUnix float64, protocol string) ([]FTXMessage, string, error) {
	p := protoFT8
	if strings.EqualFold(protocol, "FT4") {
		p = protoFT4
	}
	slot := p.slotSec
	first := math.Ceil(startUnix/slot) * slot
	dur := float64(len(samples)) / float64(rate)
	var out []FTXMessage
	var engines []string
	seen := map[string]bool{}
	for t := first; t+slot <= startUnix+dur+0.001; t += slot {
		i0 := int((t - startUnix) * float64(rate))
		i1 := i0 + int(slot*float64(rate))
		if i1 > len(samples) {
			i1 = len(samples)
		}
		msgs, engine, err := FTXDecodeAuto(ctx, samples[i0:i1], rate, p.name)
		if err != nil {
			return out, engineSummary(engines), err
		}
		engines = append(engines, engine)
		for _, m := range msgs {
			key := fmt.Sprintf("%d|%s", int64(t), m.Text)
			if seen[key] {
				continue
			}
			seen[key] = true
			m.SlotUnix = int64(t)
			out = append(out, m)
		}
	}
	return out, engineSummary(engines), nil
}

// WSPRWindowsAuto cuts a capture at even UTC minutes like WSPRWindows, then
// decodes each window with the engine the policy picks (WSPRDecodeAuto).
// Every returned message carries its WindowT and Engine; the second return
// value summarises the engines that ran.
func WSPRWindowsAuto(ctx context.Context, samples []float64, rate int, startUnix float64) ([]WSPRWindow, string, error) {
	dur := float64(len(samples)) / float64(rate)
	first := math.Ceil(startUnix/120) * 120
	var out []WSPRWindow
	var engines []string
	for t := first; t+114 <= startUnix+dur; t += 120 {
		i0 := int((t - startUnix) * float64(rate))
		i1 := i0 + 120*rate
		if i1 > len(samples) {
			i1 = len(samples)
		}
		seg := samples[i0:i1]
		w := WSPRWindow{Start: int64(t), Audio12k: resampleTo12k(seg, rate)}
		msgs, engine, err := WSPRDecodeAuto(ctx, seg, rate)
		if err != nil {
			return out, engineSummary(engines), err
		}
		engines = append(engines, engine)
		w.Messages = msgs
		for k := range w.Messages {
			w.Messages[k].WindowT = int64(t)
		}
		out = append(out, w)
	}
	return out, engineSummary(engines), nil
}
