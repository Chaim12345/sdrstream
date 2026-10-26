package pcmv4

// Offline replay: recorded UberSDR server stream -> pcmv4 decode -> complex
// IQ -> iqdemod -> audio. This exercises the same stages the monitor's
// ubersdr source runs (iqsrc.UberClient's pcmv4 decode feeding iqdemod),
// against bytes a real server encoded and upstream hash-pinned. It is NOT
// live proof: the fixture's RF content is unknown, so the assertions are
// structural (rates, sample accounting, finite output) and the measured
// energy is reported as a fact, never as a content claim.

import (
	"math"
	"testing"

	"github.com/chaim12345/sdrstream/go/internal/iqdemod"
)

func TestReplayRecordedIQThroughDemod(t *testing.T) {
	packets := readFixture(t, "testdata/pcmv4_stream.bin")
	dec := NewPCMv4StreamDecoder()
	// Gather the wideband IQ segment (384000 Hz, 2ch) as complex64.
	var iq []complex64
	var mono int
	segRates := map[[2]int]int{}
	for _, pkt := range packets {
		h, samples, err := dec.DecodePacket(pkt)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		segRates[[2]int{h.SampleRate, h.Channels}] += h.SampleCount
		switch {
		case h.Channels == 2 && h.SampleRate == 384000:
			if len(samples)%2 != 0 {
				t.Fatalf("odd interleaved count %d", len(samples))
			}
			for i := 0; i < len(samples); i += 2 {
				iq = append(iq, complex(float32(samples[i])/32768, float32(samples[i+1])/32768))
			}
		case h.Channels == 1:
			mono += len(samples)
		}
	}
	if len(iq) == 0 {
		t.Fatal("fixture carried no 384kHz IQ segment; replay stopped exercising the demod path")
	}
	t.Logf("segments (rate,ch)->samples: %v; replaying %d complex IQ samples, %d mono samples untouched",
		segRates, len(iq), mono)

	d := iqdemod.New(384000, 12000, 0, iqdemod.USB)
	audio := d.Process(iq, nil)
	// Structural contract: length tracks the rate ratio within resampler
	// tolerance, every sample finite.
	want := int(float64(len(iq)) * 12000.0 / 384000.0)
	if audio == nil || len(audio) < want*9/10 || len(audio) > want*11/10 {
		t.Fatalf("audio length %d, want ~%d from %d IQ samples", len(audio), want, len(iq))
	}
	var peak, sum2 float64
	for _, a := range audio {
		if math.IsNaN(a) || math.IsInf(a, 0) {
			t.Fatal("demod produced a non-finite sample")
		}
		if math.Abs(a) > peak {
			peak = math.Abs(a)
		}
		sum2 += a * a
	}
	rms := math.Sqrt(sum2 / float64(len(audio)))
	t.Logf("demod output: %d samples @12kHz, RMS %.5f, peak %.5f (recorded fixture content unknown; no decodability claim)", len(audio), rms, peak)
}
