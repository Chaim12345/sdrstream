package decode

import (
	"encoding/binary"
	"math"
	"os"
	"testing"
	"time"
)

// The CPR armor tests pin the global-decode zone arithmetic against a
// synthetic encoder built from the DO-260B spec. They exist because a
// review pass flagged the odd-newer branch as "wrong by ~1 km": that was a
// false positive (odd-newer legitimately decodes the position at the odd
// frame's instant, which differs from the even instant by aircraft
// movement), but the cross-check against Junzi Sun's pyModeS reference
// showed identical formulas, and these tests keep the equivalence pinned.

// cprEncode turns a ground-truth position into the raw 17-bit CPR fields
// the way a transponder does: the position modulo the zone span, quantized
// onto the 2^17 wheel.
func cprEncode(lat, lon float64, odd bool) (latCPR, lonCPR float64) {
	cprMod := func(v, span float64) float64 {
		m := math.Mod(v, span)
		if m < 0 {
			m += span
		}
		return m
	}
	dlat := 360.0 / 60.0
	if odd {
		dlat = 360.0 / 59.0
	}
	rawLat := math.Floor(131072*cprMod(lat, dlat)/dlat + 0.5)
	nl := cprNL(lat)
	dlon := 360.0 / float64(nl)
	if odd {
		dlon = 360.0 / float64(nl-1)
	}
	rawLon := math.Floor(131072*cprMod(lon, dlon)/dlon + 0.5)
	return rawLat / 131072, rawLon / 131072
}

func cprFrames(latEven, lonEven, latOdd, lonOdd float64) (even, odd *ModeSMessage) {
	evenLat, evenLon := cprEncode(latEven, lonEven, false)
	oddLat, oddLon := cprEncode(latOdd, lonOdd, true)
	even = &ModeSMessage{HasCPR: true, CPRLat: evenLat, CPRLon: evenLon}
	odd = &ModeSMessage{HasCPR: true, CPRLat: oddLat, CPRLon: oddLon, CPROdd: true}
	return even, odd
}

const cprTolerance = 2e-4 // ~20 m: half a 2^17 wheel step of the raw field

func TestCPRPositionRoundTripBothParities(t *testing.T) {
	// Ground truth: two observations ~100 m apart, both inside the same
	// NL zone (required by the cprNL equal check in CPRPosition).
	baseLat, baseLon := 52.2572, 3.9194
	lat2, lon2 := 52.2576, 3.9202
	even, odd := cprFrames(baseLat, baseLon, lat2, lon2)

	// Odd-newer: the flywheel must walk back to the odd frame's position.
	if lat, lon, err := CPRPosition(even, odd, time.Unix(1000, 0), time.Unix(1001, 0)); err != nil {
		t.Fatalf("odd-newer: %v", err)
	} else if math.Abs(lat-lat2) > cprTolerance || math.Abs(lon-lon2) > cprTolerance {
		t.Errorf("odd-newer decode = (%.6f, %.6f), want (%.6f, %.6f)", lat, lon, lat2, lon2)
	}

	// Even-newer: swapping the timestamps must decode the even instant.
	if lat, lon, err := CPRPosition(even, odd, time.Unix(1001, 0), time.Unix(1000, 0)); err != nil {
		t.Fatalf("even-newer: %v", err)
	} else if math.Abs(lat-baseLat) > cprTolerance || math.Abs(lon-baseLon) > cprTolerance {
		t.Errorf("even-newer decode = (%.6f, %.6f), want (%.6f, %.6f)", lat, lon, baseLat, baseLon)
	}

	// No movement at all between frames: both directions decode to the
	// same point, which is the case a naive timestamp comparison would
	// assume for real traffic.
	even, odd = cprFrames(baseLat, baseLon, baseLat, baseLon)
	if lat, lon, err := CPRPosition(even, odd, time.Unix(1000, 0), time.Unix(1001, 0)); err != nil {
		t.Fatalf("stationary odd-newer: %v", err)
	} else if math.Abs(lat-baseLat) > cprTolerance || math.Abs(lon-baseLon) > cprTolerance {
		t.Errorf("stationary odd-newer decode = (%.6f, %.6f), want (%.6f, %.6f)", lat, lon, baseLat, baseLon)
	}
	if lat, lon, err := CPRPosition(even, odd, time.Unix(1001, 0), time.Unix(1000, 0)); err != nil {
		t.Fatalf("stationary even-newer: %v", err)
	} else if math.Abs(lat-baseLat) > cprTolerance || math.Abs(lon-baseLon) > cprTolerance {
		t.Errorf("stationary even-newer decode = (%.6f, %.6f), want (%.6f, %.6f)", lat, lon, baseLat, baseLon)
	}
}

// TestReadWAVTruncatedDataChunk guards the torn-capture path: the data
// chunk header declares more bytes than the file holds (disk full, crash
// mid-write). The reader must trust the bytes, not the header.
func TestReadWAVTruncatedDataChunk(t *testing.T) {
	samples := make([]int16, 40)
	for i := range samples {
		samples[i] = int16(i * 100)
	}
	header := make([]byte, 44)
	copy(header[0:4], "RIFF")
	copy(header[8:12], "WAVE")
	copy(header[12:16], "fmt ")
	binary.LittleEndian.PutUint32(header[16:20], 16)
	binary.LittleEndian.PutUint16(header[20:22], 1)
	binary.LittleEndian.PutUint16(header[22:24], 1)
	binary.LittleEndian.PutUint32(header[24:28], 8000)
	binary.LittleEndian.PutUint32(header[28:32], 16000)
	binary.LittleEndian.PutUint16(header[32:34], 2)
	binary.LittleEndian.PutUint16(header[34:36], 16)
	copy(header[36:40], "data")
	binary.LittleEndian.PutUint32(header[40:44], 4000) // lies: only 80 bytes follow

	pcm := make([]byte, 2*len(samples))
	for i, s := range samples {
		binary.LittleEndian.PutUint16(pcm[2*i:2*i+2], uint16(s))
	}

	path := t.TempDir() + "/torn.wav"
	if err := os.WriteFile(path, append(header, pcm...), 0o644); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("ReadWAV panicked on a torn capture: %v", r)
		}
	}()
	got, rate, err := ReadWAV(path)
	if err != nil {
		t.Fatalf("ReadWAV: %v", err)
	}
	if rate != 8000 {
		t.Errorf("rate = %d, want 8000", rate)
	}
	if len(got) != len(samples) {
		t.Errorf("len(samples) = %d, want the %d samples that physically exist", len(got), len(samples))
	}
}
