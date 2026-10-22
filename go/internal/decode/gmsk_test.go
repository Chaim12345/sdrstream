package decode

import (
	"math"
	"math/rand"
	"testing"
)

// Synthetic modems: build an AX.25 frame, HDLC-encode it, and render it
// as GMSK-style baseband or BPSK audio so the demodulators can be
// checked end to end without a recording.

func testAX25Frame(dest, src string, info string) []byte {
	addr := func(call string, last bool) []byte {
		for len(call) < 6 {
			call += " "
		}
		var b []byte
		for i := 0; i < 6; i++ {
			b = append(b, call[i]<<1)
		}
		b = append(b, 0x60)
		if last {
			b[6] |= 1
		}
		return b
	}
	frame := append(addr(dest, false), addr(src, true)...)
	frame = append(frame, 0x03, 0xF0)
	frame = append(frame, info...)
	// AX.25 FCS: ones-complement of the CRC over the frame, LSB first.
	crc := ^crc16(frame)
	return append(frame, byte(crc), byte(crc>>8))
}

// hdlcEncode turns frame bytes into on-the-air bits: LSB first, bit
// stuffing, NRZI levels, surrounded by flags.
func hdlcEncode(frames [][]byte, flags int) []bool {
	var bits []bool
	ones := 0 // runs across bytes: stuffing must catch 1-runs over boundaries
	addByte := func(b byte, stuff bool) {
		for i := 0; i < 8; i++ {
			bit := b>>i&1 == 1
			bits = append(bits, bit)
			if bit {
				ones++
			} else {
				ones = 0
			}
			if stuff && ones == 5 {
				bits = append(bits, false)
				ones = 0
			}
		}
		if !stuff {
			ones = 0
		}
	}
	for f := 0; f < flags; f++ {
		addByte(0x7E, false)
	}
	for _, frame := range frames {
		for _, b := range frame {
			addByte(b, true)
		}
		for f := 0; f < flags; f++ {
			addByte(0x7E, false)
		}
	}
	// NRZI: 0 bit toggles the level, 1 bit holds it.
	level := false
	out := make([]bool, len(bits))
	for i, b := range bits {
		if !b {
			level = !level
		}
		out[i] = level
	}
	return out
}

// gmskWave renders NRZI levels as a filtered NRZ waveform: what an FM
// discriminator outputs for a GMSK downlink.
func gmskWave(levels []bool, rate, baud int, noise float64, seed int64) []float64 {
	spb := float64(rate) / float64(baud)
	pre := rate / 20 // 50 ms of noise before the flags, like a real capture
	full := make([]float64, pre+int(float64(len(levels))*spb))
	rng := rand.New(rand.NewSource(seed))
	for i := 0; i < pre; i++ {
		full[i] = noise * rng.NormFloat64()
	}
	out := full[pre:]
	// BT<1 filtering approximated by a one-pole low-pass at baud rate.
	alpha := 1 - math.Exp(-2*math.Pi*float64(baud)/float64(rate))
	var y float64
	for i := range out {
		bit := int(float64(i) / spb)
		if bit >= len(levels) {
			bit = len(levels) - 1
		}
		target := -1.0
		if levels[bit] {
			target = 1.0
		}
		y += alpha * (target - y)
		out[i] = y + noise*rng.NormFloat64()
	}
	return full
}

// bpskWave renders NRZI levels as BPSK on an audio carrier, with a small
// frequency error and additive noise.
func bpskWave(levels []bool, rate int, carrier, baud, freqErr, noise float64, seed int64) []float64 {
	spb := float64(rate) / baud
	pre := rate / 10 // 100 ms of noise for loop acquisition
	full := make([]float64, pre+int(float64(len(levels))*spb))
	rng := rand.New(rand.NewSource(seed))
	for i := 0; i < pre; i++ {
		full[i] = noise * rng.NormFloat64()
	}
	out := full[pre:]
	// Raised-cosine-ish smoothing: one-pole on the symbol levels.
	alpha := 1 - math.Exp(-2*math.Pi*baud*0.75/float64(rate))
	var y float64
	for i := range out {
		bit := int(float64(i) / spb)
		if bit >= len(levels) {
			bit = len(levels) - 1
		}
		target := -1.0
		if levels[bit] {
			target = 1.0
		}
		y += alpha * (target - y)
		t := float64(i) / float64(rate)
		out[i] = y*math.Cos(2*math.Pi*(carrier+freqErr)*t) + noise*rng.NormFloat64()
	}
	return full
}

func framesContain(frames []AX25, src, infoSub string) bool {
	for _, f := range frames {
		if len(f.Source) >= len(src) && f.Source[:len(src)] == src && contains(f.Info, infoSub) {
			return true
		}
	}
	return false
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	}())
}

func TestGMSKSynthetic9600(t *testing.T) {
	f1 := testAX25Frame("CQ", "RS0ISS", "=5153.50N/04022.50E-hello from the ISS")
	f2 := testAX25Frame("APRS", "N0CALL", ">testing 9600 baud gmsk")
	wave := gmskWave(hdlcEncode([][]byte{f1, f2}, 20), 48000, 9600, 0.08, 1)
	frames := GMSKFrames(wave, 48000, []int{9600})
	if !framesContain(frames, "RS0ISS", "5153.50N") {
		t.Errorf("GMSK 9600: ISS frame not decoded; got %v", frames)
	}
	if !framesContain(frames, "N0CALL", "9600 baud gmsk") {
		t.Errorf("GMSK 9600: N0CALL frame not decoded; got %v", frames)
	}
}

func TestGMSKSynthetic4800(t *testing.T) {
	f := testAX25Frame("CQ", "CAS4A", "hello from 4800")
	wave := gmskWave(hdlcEncode([][]byte{f, f, f}, 6), 48000, 4800, 0.1, 2)
	frames := GMSKFrames(wave, 48000, []int{4800})
	if !framesContain(frames, "CAS4A", "4800") {
		t.Errorf("GMSK 4800: frame not decoded; got %v", frames)
	}
}

func TestGMSKDCOffsetAndInvert(t *testing.T) {
	f := testAX25Frame("CQ", "DC1AA", "dc offset test")
	wave := gmskWave(hdlcEncode([][]byte{f}, 8), 48000, 9600, 0.05, 3)
	for i := range wave {
		wave[i] = wave[i]*3 + 0.7 // polarity, gain and offset must not matter
	}
	frames := GMSKFrames(wave, 48000, []int{9600})
	if !framesContain(frames, "DC1AA", "dc offset") {
		t.Errorf("GMSK with DC offset: frame not decoded; got %v", frames)
	}
}

func TestBPSKSynthetic1200(t *testing.T) {
	f1 := testAX25Frame("CQ", "FO29", "bpsk downlink test frame")
	f2 := testAX25Frame("CQ", "FO29", "second frame here")
	wave := bpskWave(hdlcEncode([][]byte{f1, f2}, 8), 48000, 1200, 1200, 15, 0.15, 4)
	frames := BPSKFrames(wave, 48000, []int{1200}, [2]float64{600, 2000})
	if !framesContain(frames, "FO29", "bpsk downlink") {
		t.Errorf("BPSK 1200: first frame not decoded; got %v", frames)
	}
	if !framesContain(frames, "FO29", "second frame") {
		t.Errorf("BPSK 1200: second frame not decoded; got %v", frames)
	}
}

func TestBPSKSynthetic9600(t *testing.T) {
	f := testAX25Frame("CQ", "BPSK9", "nine six hundred baud")
	wave := bpskWave(hdlcEncode([][]byte{f, f}, 8), 48000, 12000, 9600, -60, 0.2, 5)
	frames := BPSKFrames(wave, 48000, []int{9600}, [2]float64{8000, 16000})
	if !framesContain(frames, "BPSK9", "nine six") {
		t.Errorf("BPSK 9600: frame not decoded; got %v", frames)
	}
}

func TestBlobHexLines(t *testing.T) {
	f1 := testAX25Frame("CQ", "HEXSAT", "hex line one")
	f2 := testAX25Frame("CQ", "HEXSAT", "hex line two")
	blob := []byte("from satnogs demod:\n" + hexString(f1) + "\n" + hexString(f2) + "\n")
	frames := ParseBlobFrames(blob)
	if len(frames) != 2 {
		t.Fatalf("got %d frames, want 2 (%v)", len(frames), frames)
	}
	if !frames[0].CRCValid || !frames[1].CRCValid {
		t.Errorf("frames should be CRC valid: %+v", frames)
	}
	if frames[0].Info != "hex line one" || frames[1].Info != "hex line two" {
		t.Errorf("wrong payloads: %q %q", frames[0].Info, frames[1].Info)
	}
}

func TestBlobRawScan(t *testing.T) {
	f1 := testAX25Frame("CQ", "RAWSAT", "raw frame one")
	f2 := testAX25Frame("CQ", "RAWSAT", "raw frame two")
	blob := append(append([]byte{0x00, 0xFF, 0x13}, f1...), append([]byte{0x42}, f2...)...)
	frames := ParseBlobFrames(blob)
	if len(frames) != 2 {
		t.Fatalf("got %d frames, want 2 (%v)", len(frames), frames)
	}
	if frames[0].Offset != 3 || frames[1].Offset != 3+len(f1)+1 {
		t.Errorf("wrong offsets: %d %d", frames[0].Offset, frames[1].Offset)
	}
	if frames[0].Info != "raw frame one" || frames[1].Info != "raw frame two" {
		t.Errorf("wrong payloads: %q %q", frames[0].Info, frames[1].Info)
	}
}

func TestBlobLenientNoFCS(t *testing.T) {
	f := testAX25Frame("CQ", "NOFCS", "station stripped the fcs")
	blob := f[:len(f)-2] // FCS stripped, as some station software uploads
	frames := ParseBlobFrames(blob)
	if len(frames) != 1 {
		t.Fatalf("got %d frames, want 1 (%v)", len(frames), frames)
	}
	if frames[0].CRCValid {
		t.Error("frame without FCS must not be reported CRC-valid")
	}
	if frames[0].Info != "station stripped the fcs" {
		t.Errorf("wrong payload: %q", frames[0].Info)
	}
}

func hexString(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, digits[c>>4], digits[c&0xF])
	}
	return string(out)
}
