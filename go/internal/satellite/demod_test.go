package satellite

import (
	"testing"

	"github.com/chaim12345/sdrstream/go/internal/satellites"
)

// TestDemodForWeatherWideFM pins the APT demodulation rule: weather-table
// birds get narrow-FM with an explicit 40 kHz bandwidth on OpenWebRX,
// amateur FM voice stays at the narrow default, and WebSDR never gets an
// OpenWebRX-style bandwidth parameter.
func TestDemodForWeatherWideFM(t *testing.T) {
	weather := satellites.Sat{Name: "NOAA 15", NoradID: 25338,
		DownlinkHz: 137620000, Mode: "FM", Group: "weather"}
	amateur := satellites.Sat{Name: "SO-50", DownlinkHz: 436795000, Mode: "FM"}

	mode, bw := demodFor("openwebrx", weather)
	if mode != "nfm" || bw == nil || *bw != 40000 {
		t.Fatalf("weather on openwebrx: got %s, %v; want nfm, 40000", mode, bw)
	}

	mode, bw = demodFor("openwebrx", amateur)
	if mode != "nfm" || bw != nil {
		t.Fatalf("amateur on openwebrx: got %s, %v; want nfm, nil", mode, bw)
	}

	mode, bw = demodFor("websdr", weather)
	if mode != "FM" || bw != nil {
		t.Fatalf("weather on websdr: got %s, %v; want FM, nil", mode, bw)
	}
}
