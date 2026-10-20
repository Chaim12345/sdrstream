package decode

import (
	"strings"
	"testing"
)

// refAPRS is a real Bell-202 AFSK1200 AX.25 capture generated with
// direwolf's gen_packets and committed under tests/data. The pure-Go
// demodulator must recover both frames without multimon-ng.
const refAPRS = "../../../tests/data/ref_aprs.wav"

func TestAFSKReference(t *testing.T) {
	packets, err := AFSKFrames(refAPRS)
	if err != nil {
		t.Fatalf("AFSKFrames: %v", err)
	}
	if len(packets) != 2 {
		t.Fatalf("got %d packets, want 2: %+v", len(packets), packets)
	}
	first := packets[0]
	if first.Source != "M0XXX-9" {
		t.Errorf("source = %q, want M0XXX-9", first.Source)
	}
	if first.Destination != "APZSAT-0" {
		t.Errorf("destination = %q, want APZSAT-0", first.Destination)
	}
	if !strings.Contains(first.Info, "Hello from space!") {
		t.Errorf("info = %q, want it to contain %q", first.Info, "Hello from space!")
	}
}

func TestFindBurstsNoPanic(t *testing.T) {
	// An even number of subsampled power values used to index past the
	// end of the slice while taking the median.
	samples, rate, err := ReadWAV(refAPRS)
	if err != nil {
		t.Fatalf("ReadWAV: %v", err)
	}
	if bursts := findBursts(samples, rate, rate/2); len(bursts) == 0 {
		t.Log("no bursts found (tolerated; gating is optional)")
	}
}
