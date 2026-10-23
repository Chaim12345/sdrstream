package tracking

import (
	"strings"
	"testing"
	"time"
)

// A clock-only summary ("02:07:16 rise") reads as a past pass when the plan
// is printed mid-morning for a pass after midnight tomorrow. The date must
// ride along so a future pass can never masquerade as a missed one.
func TestPassSummaryCarriesDate(t *testing.T) {
	rise := time.Date(2026, 10, 24, 2, 7, 16, 0, time.UTC)
	p := Pass{Rise: rise, Peak: LookAngle{Time: rise.Add(5 * time.Minute), Elevation: 25.1}, DurationSec: 620}
	got := p.Summary()
	if !strings.Contains(got, "2026-10-24 02:07:16") {
		t.Fatalf("summary lost the date: %q", got)
	}
}
