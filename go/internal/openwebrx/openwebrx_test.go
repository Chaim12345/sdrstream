package openwebrx

import (
	"encoding/binary"
	"math"
	"testing"
)

// TestDecodeFloat32 checks that an uncompressed FFT frame (raw little-endian
// float32 dB values) is read as floats, not as int16. Reading it as int16 was
// the original bug: the high half of each float looked like a constant.
func TestDecodeFloat32(t *testing.T) {
	values := []float32{-48.5, -17.75, 3.25, 0}
	payload := make([]byte, len(values)*4)
	for i, v := range values {
		binary.LittleEndian.PutUint32(payload[i*4:], math.Float32bits(v))
	}
	got := decodeFloat32(payload)
	if len(got) != len(values) {
		t.Fatalf("len = %d, want %d", len(got), len(values))
	}
	for i, v := range values {
		if got[i] != float64(v) {
			t.Errorf("bin %d = %v, want %v", i, got[i], v)
		}
	}
}

func TestAverageSpectra(t *testing.T) {
	avg := AverageSpectra([][]float64{{1, 2, 3}, {3, 4, 5}})
	want := []float64{2, 3, 4}
	for i := range want {
		if avg[i] != want[i] {
			t.Errorf("bin %d = %v, want %v", i, avg[i], want[i])
		}
	}
	// Frames longer than the shortest are truncated, not panicked on.
	avg = AverageSpectra([][]float64{{1, 2}, {3, 4, 5}})
	if len(avg) != 2 || avg[0] != 2 || avg[1] != 3 {
		t.Errorf("truncated average = %v, want [2 3]", avg)
	}
}

func TestFindSpectrumPeaks(t *testing.T) {
	// Noise floor at -60 dB with a single spike at bin 10.
	bins := make([]float64, 64)
	for i := range bins {
		bins[i] = -60
	}
	bins[10] = -20
	centre, span := 100e6, 2e6
	peaks := FindSpectrumPeaks(bins, centre, span, 8)
	if len(peaks) != 1 {
		t.Fatalf("got %d peaks, want 1: %+v", len(peaks), peaks)
	}
	binHz := span / float64(len(bins))
	wantHz := centre - span/2 + (10+0.5)*binHz
	if math.Abs(peaks[0].FreqHz-wantHz) > 1 {
		t.Errorf("peak at %v Hz, want %v Hz", peaks[0].FreqHz, wantHz)
	}
	if peaks[0].LevelDB != -20 {
		t.Errorf("peak level = %v, want -20", peaks[0].LevelDB)
	}
}
