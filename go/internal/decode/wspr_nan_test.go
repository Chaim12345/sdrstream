package decode

import (
	"testing"
)

// TestWSPRSyncDemodConstantPowerNoNaN: when the demodulated symbol powers
// are all identical (here: silence), the normalization variance is zero
// and used to divide NaN into every soft symbol. The guard must leave the
// midpoint value instead.
func TestWSPRSyncDemodConstantPowerNoNaN(t *testing.T) {
	np := 162 * 256
	id := make([]float64, np)
	qd := make([]float64, np)
	f1 := 0.0
	shift := 0
	drift := 0.0
	_, symbols := wsprSyncDemod(id, qd, np, &f1, 0, 0, 0, &shift, -10, 10, 5, &drift, 1.0, 2)
	for i, s := range symbols {
		if s != 128 {
			t.Fatalf("symbol %d = %d, want the 128 midpoint for zero-power input", i, s)
		}
	}
}
