package satellite

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/chaim12345/sdrstream/go/internal/catalogue"
	"github.com/chaim12345/sdrstream/go/internal/satellites"
)

// TestChaseLiveGids runs a real 30-second chase against gids.nl: profile
// select, tune to the NOAA APT frequency, record, retune. Gated on LIVE=1.
func TestChaseLiveGids(t *testing.T) {
	if os.Getenv("LIVE") != "1" {
		t.Skip("set LIVE=1 to run against the real receiver")
	}
	now := time.Now().UTC()
	plan := ChasePlan{
		Satellite: satellites.Sat{
			Name: "NOAA 15", NoradID: 25338,
			DownlinkHz: 137620000, Mode: "FM", Group: "weather",
		},
		Receiver:    catalogue.VerifiedVHF[0], // gids.nl
		Start:       now,
		End:         now.Add(30 * time.Second),
		StartFreqHz: 137620000,
	}
	out := t.TempDir() + "/chase.wav"
	n, err := Chase(context.Background(), plan, out, 1500, 5*time.Second, 10, false, func(s string) { t.Log(s) })
	if err != nil {
		t.Fatalf("chase: %v", err)
	}
	if n < 8000 {
		t.Fatalf("only %d samples recorded", n)
	}
	info, err := os.Stat(out)
	if err != nil || info.Size() < 16000 {
		t.Fatalf("wav: %v %v", info, err)
	}
	t.Logf("recorded %d samples to %s (%d bytes)", n, out, info.Size())
}
