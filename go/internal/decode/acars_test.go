package decode

import (
	"math"
	"math/rand"
	"os"
	"testing"
)

// TestACARSRealRecording decodes the acarsdec project's real 4-channel
// test.wav (LGPL, not vendored here). Set SDRSTREAM_ACARS_WAV to it. The
// reference acarsdec output lists 7 blocks; the CRC+parity gate must
// reproduce them.
func TestACARSRealRecording(t *testing.T) {
	p := os.Getenv("SDRSTREAM_ACARS_WAV")
	if p == "" {
		t.Skip("SDRSTREAM_ACARS_WAV not set")
	}
	chs, rate, err := ReadWAVChannels(p)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for c, s := range chs {
		for _, m := range ACARSDecode(s, rate, c) {
			total++
			t.Logf("ch%d %.2fs %s", c, m.AtSec, m)
		}
	}
	if total < 7 {
		t.Fatalf("decoded %d blocks, acarsdec reference has 7", total)
	}
}

func TestACARSNoiseYieldsNothing(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	s := make([]float64, 12500*20)
	for i := range s {
		s[i] = rng.NormFloat64()
	}
	if m := ACARSDecode(s, 12500, 0); len(m) != 0 {
		t.Fatalf("noise decoded %d blocks", len(m))
	}
}

// acarsModulate builds a clean test signal from the line code: data bits
// are differentially coded (a tone of 2400 Hz keeps the bit, 1200 Hz flips
// it) and the phase is continuous across bits.
func acarsModulate(block []byte, lead int) []float64 {
	var data []bool
	push := func(c byte, withParity bool) {
		if withParity && !oddParity(c) {
			c |= 0x80
		}
		for i := 0; i < 8; i++ {
			data = append(data, c>>i&1 == 1)
		}
	}
	for i := 0; i < 20; i++ {
		data = append(data, true) // pre-key
	}
	for _, c := range []byte{0x2b, 0x2a, 0x16, 0x16, 0x01} {
		push(c, true)
	}
	var crc uint16
	for _, c := range block {
		push(c, true)
		if !oddParity(c) {
			c |= 0x80
		}
		crc = acarsCRCUpdate(crc, c)
	}
	push(byte(crc), false)
	push(byte(crc>>8), false)
	push(0x7f, false)
	for i := 0; i < 8; i++ {
		data = append(data, true)
	}
	spb := float64(acarsRate) / acarsBaud
	out := make([]float64, lead+int(float64(len(data)+4)*spb))
	phase := 0.0
	n := lead
	// Tone high when the bit equals the previous one.
	prev := true
	for k, d := range data {
		f := 1200.0
		if d == prev {
			f = 2400
		}
		prev = d
		end := lead + int(float64(k+1)*spb)
		for ; n < end && n < len(out); n++ {
			phase += 2 * math.Pi * f / acarsRate
			out[n] = 0.5 * math.Sin(phase)
		}
	}
	return out
}

func TestACARSSyntheticRoundTrip(t *testing.T) {
	body := []byte("E.N123AB\x15" + "H1" + "7\x02" + "TEST MESSAGE 123\x03")
	for _, lead := range []int{500, 503, 507} {
		x := acarsModulate(body, lead)
		got := ACARSDecode(x, acarsRate, 0)
		if len(got) != 1 {
			t.Fatalf("lead %d: decoded %d blocks", lead, len(got))
		}
		m := got[0]
		if m.Reg != "N123AB" || m.Label != "H1" || m.Text != "TEST MESSAGE 123" {
			t.Fatalf("lead %d: %+v", lead, m)
		}
	}
}
