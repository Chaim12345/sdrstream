package decode

import (
	"strings"
	"testing"
)

func TestPSK31ScanReferenceFile(t *testing.T) {
	samples, rate, err := ReadWAV(refPSK31)
	if err != nil {
		t.Skip("reference wav absent")
	}
	sigs := PSK31Scan(samples, rate)
	found := false
	for _, s := range sigs {
		t.Logf("%.1f Hz snr %.1f invalid %d: %q", s.FreqHz, s.SNR, s.Invalid, s.Text)
		if strings.Contains(s.Text, "DE RS44 PSK31 TEST") {
			found = true
		}
	}
	if !found {
		t.Fatalf("scan did not recover the sent text: %+v", sigs)
	}
}
