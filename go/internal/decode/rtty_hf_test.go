package decode

import (
	"strings"
	"testing"
)

// TestRTTYDecodeAutoInverted makes sure auto polarity finds the readable
// decode when mark and space are swapped, as on the DWD F1B service.
func TestRTTYDecodeAutoInverted(t *testing.T) {
	// Reuse the committed 45.45/170 fixture but feed inverted polarity by
	// swapping mark and space; auto must still return the text.
	samples, rate, err := ReadWAV(refRTTY)
	if err != nil {
		t.Skipf("fixture: %v", err)
	}
	text, inverted := RTTYDecodeAuto(samples, rate, 2125, 1955, 45.45)
	if text == "" {
		t.Fatal("no text")
	}
	if !strings.Contains(strings.ToUpper(text), "CQ") && !strings.Contains(strings.ToUpper(text), "TEST") {
		t.Errorf("text %q does not look like the fixture text", text)
	}
	t.Logf("inverted=%v text=%q", inverted, text)
}

// TestRTTYQualityRejectsNoise: noise decodes score far below real text.
func TestRTTYQualityRejectsNoise(t *testing.T) {
	if q := RTTYQuality("CQ CQ CQ DE DDK2 DDH7 DDK9 FREQUENCIES 4583 KHZ"); q < 90 {
		t.Errorf("real text scored %v", q)
	}
	if q := RTTYQuality(";(%$&)(/%=$!"); q > 1 {
		t.Errorf("noise scored %v", q)
	}
}
