package wfmlisten

// Synthetic broadcast-FM capture generator for offline chain validation:
// L=1kHz tone, R=2.5kHz tone, 10% pilot, standard sin/sin subcarrier
// convention, +/-75kHz deviation, carrier at +100kHz offset, 1.5 Msps f32.

import (
	"encoding/binary"
	"math"
	"os"
	"testing"
)

func TestSynthBroadcastCapture(t *testing.T) {
	out := os.Getenv("SDRSTREAM_WFM_SYNTH_OUT")
	if out == "" {
		t.Skip("set SDRSTREAM_WFM_SYNTH_OUT")
	}
	const rate = 1500000
	n := rate * 3
	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var phase float64
	var b [8]byte
	for i := 0; i < n; i++ {
		tm := float64(i) / rate
		l := math.Sin(2 * math.Pi * 1000 * tm)
		r := math.Sin(2 * math.Pi * 2500 * tm)
		mpx := 0.45*((l+r)/2) + 0.1*math.Sin(2*math.Pi*19000*tm) + 0.45*((l-r)/2)*math.Sin(2*math.Pi*38000*tm)
		phase += 2 * math.Pi * (100000 + 75000*mpx) / rate
		// Optional gap pattern mimicking the real capture: drop bursts so
		// total coverage ~50% (SDRSTREAM_WFM_SYNTH_GAP=1). Burst ~9ms every
		// ~17ms of stream => 88-gap-like cadence over 15s scales to 3s here.
		if os.Getenv("SDRSTREAM_WFM_SYNTH_GAP") == "1" {
			period := rate / 60      // ~16.7ms cadence
			burst := period / 2      // 50% duty
			if i%period < burst {
				continue // sample never written: the receiver's gap
			}
		}
		binary.LittleEndian.PutUint32(b[0:4], math.Float32bits(float32(math.Cos(phase))))
		binary.LittleEndian.PutUint32(b[4:8], math.Float32bits(float32(math.Sin(phase))))
		f.Write(b[:])
	}
	t.Logf("wrote %s (%d samples, 3s)", out, n)
}
