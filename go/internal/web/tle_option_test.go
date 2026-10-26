package web

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// The TLE option seam must keep cmd startup tests off the network: with an
// injected loader, New must call it instead of tle.Load.
func TestWithTLELoadersSeam(t *testing.T) {
	var groupCalls, catnrCalls atomic.Int32
	s := New("", "", WithTLELoaders(
		func(group string, maxAgeHours float64, offline bool, timeout time.Duration) (string, error) {
			groupCalls.Add(1)
			return "", errors.New("fixture: no network")
		},
		func(noradID int, maxAgeHours float64, offline bool, timeout time.Duration) (string, error) {
			catnrCalls.Add(1)
			return "", errors.New("fixture: no network")
		},
	))
	t.Cleanup(func() { s.cancel() })
	if groupCalls.Load() == 0 {
		t.Fatal("injected group TLE loader was never called - seam not wired")
	}
	// The erroring fixture means no passes could be built; the server must
	// still be usable (loadPasses failure is non-fatal by design).
	if got := s.buildMapData(); got == nil {
		t.Fatal("buildMapData returned nil with an erroring TLE fixture")
	}
}

// Default construction must keep the real loaders (production behavior).
func TestTLELoadersDefault(t *testing.T) {
	s := New("", "")
	t.Cleanup(func() { s.cancel() })
	if s.tleLoad == nil || s.tleLoadCatnr == nil {
		t.Fatal("default TLE loaders not installed")
	}
}
