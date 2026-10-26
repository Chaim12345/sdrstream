package decode

// dispatcher_test.go: synthetic tests for the dispatch accounting paths.
// EVERY stream here is generated in-test (buildStream/G3RUHScramble/hand-
// built HDLC); none is an over-the-air capture. Acceptance therefore proves
// self-consistency of the dispatch wiring and of the RS/CRC checks, not RF
// interoperability. The one independent-implementation anchor stays the
// libfec RS vectors used by deframe_test.go / ax100_test.go; the AX100
// case below reuses one of those codewords.

import (
	"bytes"
	"encoding/hex"
	"math"
	"math/rand"
	"os"
	"strings"
	"testing"
)

// TestDispatcherCCSDSConsistent pins the accounting path to the primitive:
// same input must accept exactly the frames CCSDSDeframe returns, with the
// same payloads and correction counts.
func TestDispatcherCCSDSConsistent(t *testing.T) {
	r := rand.New(rand.NewSource(11))
	for _, tc := range []struct {
		il, flips int
		inv       bool
	}{{1, 0, false}, {1, 12, false}, {4, 16, false}, {2, 3, true}} {
		d := make([]byte, 223*tc.il)
		r.Read(d)
		bits := buildStream([][]byte{d}, tc.il, 77, tc.inv, tc.flips, r)
		rep, err := DispatchBits("ccsds", bits, DispatchOptions{DataLen: len(d), Interleave: tc.il})
		if err != nil {
			t.Fatal(err)
		}
		want := CCSDSDeframe(bits, len(d), tc.il, false, 2)
		got := rep.Accepted()
		if len(got) != len(want) {
			t.Fatalf("%+v: dispatcher accepted %d, primitive %d", tc, len(got), len(want))
		}
		for i := range want {
			if !bytes.Equal(got[i].Payload, want[i].Data) || got[i].Corrected != want[i].Corrected ||
				got[i].Offset != want[i].Offset || got[i].Inverted != want[i].Inverted {
				t.Fatalf("%+v frame %d: dispatcher/primitive mismatch", tc, i)
			}
			if got[i].Verified != VerifyRSCCSDS {
				t.Fatalf("%+v: verified label %q", tc, got[i].Verified)
			}
		}
	}
}

// TestDispatcherCCSDSRejectsHonest: a frame corrupted past RS capacity and
// a truncated frame must be reported as rejected candidates with the right
// reasons, not silently dropped.
func TestDispatcherCCSDSRejectsHonest(t *testing.T) {
	r := rand.New(rand.NewSource(12))
	d := make([]byte, 223)
	r.Read(d)
	bits := buildStream([][]byte{d}, 1, 30, false, 0, r)
	// Corrupt 20 distinct codeword bytes: RS(255,223) fixes at most 16.
	for i := 0; i < 20; i++ {
		p := 30 + 32 + i*8 // one bit in each of the first 20 data bytes
		bits[p] = !bits[p]
	}
	rep, err := DispatchBits("ccsds", bits, DispatchOptions{DataLen: 223})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Accepted()) != 0 {
		t.Fatal("over-corrupted frame accepted")
	}
	rej := rep.Rejected()
	if len(rej) != 1 || rej[0].Reason != RejectRSUncorrectable {
		t.Fatalf("rejects=%+v", rej)
	}
	// Truncate the stream mid-frame: same ASM, not enough bits.
	trunc := buildStream([][]byte{d}, 1, 30, false, 0, r)
	trunc = trunc[:len(trunc)-100]
	rep2, _ := DispatchBits("ccsds", trunc, DispatchOptions{DataLen: 223})
	rej2 := rep2.Rejected()
	if len(rep2.Accepted()) != 0 || len(rej2) != 1 || rej2[0].Reason != RejectTruncated {
		t.Fatalf("truncated: acc=%d rej=%+v", len(rep2.Accepted()), rej2)
	}
	// Noise after a false ASM: rejected, and the summary names the reason.
	noise := make([]bool, 3000)
	for i := range noise {
		noise[i] = r.Intn(2) == 1
	}
	copy(noise[100:], bitsOf([]byte{0x1A, 0xCF, 0xFC, 0x1D}))
	rep3, _ := DispatchBits("ccsds", noise, DispatchOptions{DataLen: 223, ASMMaxErr: 0})
	if len(rep3.Accepted()) != 0 {
		t.Fatal("noise accepted")
	}
	if s := rep3.Summary(); !strings.Contains(s, "rejected") {
		t.Fatalf("summary %q", s)
	}
}

// TestDispatcherAX100Consistent pins the accounting path to AX100RSDeframe
// using the libfec-derived shortened codeword (independent software
// reference vector, not an RF capture).
func TestDispatcherAX100Consistent(t *testing.T) {
	raw, _ := os.ReadFile("testdata/rs_shortened_libfec_vectors.txt")
	cw, _ := hex.DecodeString(strings.Fields(strings.Split(string(raw), "\n")[0])[1])
	payload := cw[:len(cw)-32]
	L := byte(len(cw) + 1)
	frame := append([]byte{0x93, 0x0B, 0x51, 0xDE, L}, cw...)
	r := rand.New(rand.NewSource(21))
	var pre []bool
	for i := 0; i < 53; i++ {
		pre = append(pre, r.Intn(2) == 1)
	}
	plain := append(pre, bitsOf(frame)...)
	plain = append(plain, bitsOf([]byte{0, 0, 0, 0})...)
	tx := G3RUHScramble(plain)
	rep, err := DispatchBits("ax100", tx, DispatchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := AX100RSDeframe(tx, 2)
	got := rep.Accepted()
	if len(got) != len(want) || len(got) != 1 {
		t.Fatalf("dispatcher %d, primitive %d", len(got), len(want))
	}
	if !bytes.Equal(got[0].Payload, payload) || got[0].Verified != VerifyRSAX100 {
		t.Fatalf("payload/verified mismatch")
	}
	// A garbage length byte must be a reported rejection, not a drop.
	bad := append([]byte{0x93, 0x0B, 0x51, 0xDE, 0x05}, cw...) // L=5 < 34
	tx2 := G3RUHScramble(append(pre, bitsOf(bad)...))
	rep2, _ := DispatchBits("ax100", tx2, DispatchOptions{})
	rej := rep2.Rejected()
	if len(rep2.Accepted()) != 0 || len(rej) != 1 || rej[0].Reason != RejectBadLength {
		t.Fatalf("bad-length: acc=%d rej=%+v", len(rep2.Accepted()), rej)
	}
}

// buildAX25Bits returns one flag-delimited, bit-stuffed AX.25 frame as
// bits (no scrambling), plus trailing garbage bits that must NOT decode.
func buildAX25Bits(t *testing.T, payload []byte) []bool {
	t.Helper()
	// Minimal I-frame header: 14 address/control bytes + PID.
	hdr := []byte{0x82, 0xA0, 0xA4, 0xA6, 0x40, 0x40, 0xE0, 0x96, 0x8C, 0xAA, 0x92, 0xA8, 0x8A, 0x61, 0x03, 0xF0}
	body := append(append([]byte{}, hdr...), payload...)
	// FCS: crc16 over body, complemented, LSB-first per AX.25.
	c := crc16(body) ^ 0xFFFF
	full := append(body, byte(c), byte(c>>8))
	// Bit-stuff and flag-delimit, LSB-first like destuff expects.
	var bits []bool
	appendFlag := func() {
		for i := 0; i < 8; i++ {
			bits = append(bits, (0x7E>>uint(i))&1 == 1)
		}
	}
	for i := 0; i < 8; i++ { // preamble flags: demodulator PLL lock-in
		appendFlag()
	}
	ones := 0
	for _, b := range full {
		for i := 0; i < 8; i++ {
			bit := (b >> uint(i)) & 1
			if ones == 5 {
				bits = append(bits, false) // stuffed zero
				ones = 0
			}
			bits = append(bits, bit == 1)
			if bit == 1 {
				ones++
			} else {
				ones = 0
			}
		}
	}
	appendFlag()
	appendFlag()
	return bits
}

// TestDispatcherAX25Accounting: a good frame is accepted with the CRC
// label; a CRC-corrupted copy is rejected with the CRC reason; both are
// reported as candidates.
func TestDispatcherAX25Accounting(t *testing.T) {
	good := buildAX25Bits(t, []byte("sdrstream dispatcher synthetic test"))
	rep, err := DispatchBits("ax25", good, DispatchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	acc := rep.Accepted()
	if len(acc) != 1 || acc[0].Verified != VerifyCRC16AX25 {
		t.Fatalf("acc=%+v", acc)
	}
	ax, ok := parseAX25(acc[0].Payload)
	if !ok || !strings.Contains(string(ax.Payload), "dispatcher synthetic") {
		t.Fatalf("parseAX25 failed: %v %q", ok, ax.Payload)
	}
	// Same frame with a payload bit flipped (64 preamble + 8 flag + 16
	// header bytes = bit 200 lands in the information field): the frame
	// candidate must be reported and rejected on CRC.
	bad := append([]bool{}, good...)
	bad[200] = !bad[200]
	rep2, _ := DispatchBits("ax25", bad, DispatchOptions{})
	crcRej := 0
	for _, f := range rep2.Rejected() {
		if f.Reason == RejectCRC {
			crcRej++
		}
	}
	if len(rep2.Accepted()) != 0 || crcRej != 1 {
		t.Fatalf("crc case: acc=%d crcRejects=%d report=%s", len(rep2.Accepted()), crcRej, rep2.Summary())
	}
	// Pure noise: no flags, no candidates - an empty report, not an error.
	r := rand.New(rand.NewSource(31))
	noise := make([]bool, 5000)
	for i := range noise {
		noise[i] = r.Intn(2) == 1
	}
	rep3, _ := DispatchBits("ax25", noise, DispatchOptions{})
	if len(rep3.Accepted()) != 0 {
		t.Fatalf("noise accepted %d frames", len(rep3.Accepted()))
	}
	for _, f := range rep3.Rejected() {
		if f.Reason == "" {
			t.Fatal("rejected candidate without a reason")
		}
	}
}

// TestDispatcherSamplesAFSK: end-to-end audio path (AFSK Bell 202 tone ->
// fskDemod -> HDLC accounting). The tone is synthesized in-test; this is a
// self-consistency check of the wiring, not an RF capture.
func TestDispatcherSamplesAFSK(t *testing.T) {
	bits := buildAX25Bits(t, []byte("afsk audio path synthetic"))
	rate := 22050
	samples := afskTone(bits, rate)
	rep, err := DispatchSamples("afsk-ax25", samples, rate, DispatchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	acc := rep.Accepted()
	if len(acc) != 1 {
		t.Fatalf("afsk path: %s", rep.Summary())
	}
	if !bytes.Contains(acc[0].Payload, []byte("afsk audio path synthetic")) {
		t.Fatalf("payload mismatch")
	}
}

// TestDispatcherUnknownProtocols: unimplemented wiring is an explicit
// error, never a silent empty report.
func TestDispatcherUnknownProtocols(t *testing.T) {
	if _, err := DispatchBits("lora", nil, DispatchOptions{}); err == nil {
		t.Fatal("lora bits: expected error")
	}
	if _, err := DispatchSamples("lora", nil, 48000, DispatchOptions{}); err == nil {
		t.Fatal("lora audio: expected error")
	}
	if _, err := DispatchBits("ccsds", nil, DispatchOptions{DataLen: 0}); err == nil {
		t.Fatal("ccsds without DataLen: expected error")
	}
}

// afskTone synthesizes Bell 202 AFSK (1200 Hz mark / 2200 Hz space) from
// an HDLC bit stream with AX.25 NRZI encoding (a 0 bit toggles the tone,
// a 1 bit holds it). Purely synthetic; used to check the audio->bits->
// frames wiring end to end.
func afskTone(bits []bool, rate int) []float64 {
	levels := make([]bool, len(bits)) // true = mark
	lvl := true
	for i, b := range bits {
		if !b {
			lvl = !lvl
		}
		levels[i] = lvl
	}
	n := int(float64(len(bits))*float64(rate)/1200) + rate/10
	out := make([]float64, n)
	var phase float64
	for i := range out {
		bi := i * 1200 / rate
		if bi >= len(levels) {
			bi = len(levels) - 1
		}
		f := 1200.0
		if !levels[bi] {
			f = 2200.0
		}
		phase += 2 * math.Pi * f / float64(rate)
		out[i] = 0.8 * math.Sin(phase)
	}
	return out
}

// bpskTone synthesizes a BPSK audio-IF carrier: the bit stream is NRZI
// encoded (0 toggles the phase, 1 holds it) and modulated as 0/pi phase of
// a carrierHz tone. Purely synthetic; checks the BPSK carrier-recovery ->
// demod -> deframe wiring, not RF interoperability.
func bpskTone(bits []bool, rate int, carrierHz, baud float64) []float64 {
	levels := make([]bool, len(bits))
	lvl := false
	for i, b := range bits {
		if !b {
			lvl = !lvl
		}
		levels[i] = lvl
	}
	n := int(float64(len(bits))*float64(rate)/baud) + rate/20
	out := make([]float64, n)
	var phase float64
	for i := range out {
		bi := int(float64(i) * baud / float64(rate))
		if bi >= len(levels) {
			bi = len(levels) - 1
		}
		ph := 0.0
		if levels[bi] {
			ph = math.Pi
		}
		phase += 2 * math.Pi * carrierHz / float64(rate)
		out[i] = 0.8 * math.Cos(phase+ph)
	}
	return out
}

// TestDispatcherSamplesBPSKCCSDS: synthetic BPSK audio carrying an
// NRZI-encoded CCSDS frame must come back through the dispatcher accepted
// and RS-verified. Self-consistency of the audio->bits->RS wiring only.
func TestDispatcherSamplesBPSKCCSDS(t *testing.T) {
	r := rand.New(rand.NewSource(41))
	d := make([]byte, 223)
	r.Read(d)
	bits := buildStream([][]byte{d}, 1, 40, false, 0, r)
	// Idle lead-in so the carrier recovery and PLL see a steady carrier.
	lead := make([]bool, 32)
	for i := range lead {
		lead[i] = true
	}
	stream := append(lead, bits...)
	samples := bpskTone(stream, 48000, 4800, 1200)
	rep, err := DispatchSamples("bpsk-ccsds", samples, 48000, DispatchOptions{DataLen: 223, Baud: 1200})
	if err != nil {
		t.Fatal(err)
	}
	acc := rep.Accepted()
	if len(acc) != 1 || !bytes.Equal(acc[0].Payload, d) {
		t.Fatalf("bpsk-ccsds: %s (carrier %.0f)", rep.Summary(), rep.Carrier)
	}
	if rep.Carrier < 4700 || rep.Carrier > 4900 {
		t.Fatalf("carrier %.0f Hz", rep.Carrier)
	}
}
