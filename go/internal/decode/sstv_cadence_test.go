package decode

import (
	"math/rand"
	"testing"
)

// Random 1200 Hz purity runs (10-20 ms bursts) at the given rate over a
// 60 s trace: the shape QA used to probe the cadence gate.
func randomPurityRuns(ratePerSec float64, seed int64) []float64 {
	rng := rand.New(rand.NewSource(seed))
	const hop = 0.01
	p := make([]float64, int(60.0/hop))
	for k := 0; k < int(ratePerSec*60); k++ {
		start := rng.Intn(len(p) - 3)
		for i := start; i < start+1+rng.Intn(2) && i < len(p); i++ {
			p[i] = 1.0
		}
	}
	return p
}

// Sparse random 1200 Hz energy must not pass the sync-cadence gate: a
// false accept here is what lets noise or speech that happens to hold a
// parity-valid VIS header read as an SSTV frame. Real frames fill the
// grid (covered by TestSSTVDetectToleratesHardEdges and friends).
func TestSSTVCadenceRejectsRandomPurityRuns(t *testing.T) {
	for _, rate := range []float64{0.5, 1.0} {
		for seed := int64(0); seed < 40; seed++ {
			if hasSyncCadence(randomPurityRuns(rate, seed), 0.01, 0.5) {
				t.Fatalf("rate %.1f runs/s seed %d: random purity runs passed the cadence gate", rate, seed)
			}
		}
	}
}
