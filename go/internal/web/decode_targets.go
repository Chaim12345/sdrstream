package web

import (
	"github.com/chaim12345/sdrstream/go/internal/decode"
	"net/http"
)

// DecodeTarget is one tap-to-monitor preset: a known HF data service
// from the curated target list with a narrow sweep window around its
// dial, so the monitor lands where decodable data modes actually live.
type DecodeTarget struct {
	Name       string  `json:"name"`
	Kind       string  `json:"kind"`
	Mode       string  `json:"mode"`
	CarrierKHz float64 `json:"carrier_khz"`
	DialKHz    float64 `json:"dial_khz"`
	ListenSec  float64 `json:"listen_sec"`
	Expect     string  `json:"expect,omitempty"`
	StartHz    float64 `json:"start_hz"`
	EndHz      float64 `json:"end_hz"`
}

// decodeTargetWindowHz keeps the sweep narrow: +/-5 kHz around the dial
// covers the data segment (FT8 fills ~3 kHz above the dial) without
// re-introducing the wide-sweep skirt-peak churn.
const decodeTargetWindowHz = 5_000

// handleDecodeTargets serves the curated list. The single source of
// truth is decode.HFBroadcasts - this handler adds only the window
// arithmetic, never a second copy of the frequencies.
func (s *Server) handleDecodeTargets(w http.ResponseWriter, r *http.Request) {
	out := make([]DecodeTarget, 0, len(decode.HFBroadcasts))
	for _, t := range decode.HFBroadcasts {
		dialHz := t.DialKHz * 1e3
		out = append(out, DecodeTarget{
			Name: t.Name, Kind: t.Kind, Mode: t.Mode,
			CarrierKHz: t.CarrierKHz, DialKHz: t.DialKHz,
			ListenSec: t.Listen.Seconds(), Expect: t.Expect,
			StartHz: dialHz - decodeTargetWindowHz,
			EndHz:   dialHz + decodeTargetWindowHz,
		})
	}
	writeJSON(w, out)
}
