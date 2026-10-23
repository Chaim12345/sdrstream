package decode

import (
	"math"
	"math/big"
	"math/rand"
	"testing"
)

// R5: audio-level FT8 SNR fixtures. The decoder's real-audio tests use
// recordings; here a fully synthetic FT8 signal is generated in-test
// (free-text payload -> CRC-14 -> systematic LDPC(174,91) -> Gray-mapped
// 8-FSK with Costas sync), mixed with calibrated AWGN, and run through
// the production FTXDecode. The encode side derives its parity from the
// repo's own parity tables by GF(2) elimination, so a table/encode bug
// cannot agree with itself: the decoder must still find the Costas
// pattern, recover soft bits, pass LDPC and verify the CRC.
//
// SNR calibration (WSJT-X convention): the reported SNR is signal power
// over noise power referred to a 2500 Hz bandwidth. A unit-amplitude
// tone has power 1/2; white noise with per-sample variance s^2 at
// 12000 Hz spreads over the 6000 Hz Nyquist band, so the in-band noise
// power is s^2 * 2500/6000 and
//
//	SNR_dB = 10*log10( (1/2) / (s^2 * 2500/6000) )
//
// solved for s below. This is an AWGN channel only - no fading, no
// doppler - so numbers are an upper bound on real performance, labelled
// synthetic.

// ft8EncodeFreeText builds the 174-bit FT8 codeword for a free-text
// message (i3=0, n3=0) using the repo parity tables.
func ft8EncodeFreeText(t *testing.T, text string) [174]uint8 {
	t.Helper()
	if len(text) > 13 {
		t.Fatalf("free text limited to 13 chars: %q", text)
	}
	// N = sum c[i] * 42^(12-i), the inverse of the base-42 unpacking.
	n := big.NewInt(0)
	base := big.NewInt(42)
	for i := 0; i < 13; i++ {
		n.Mul(n, base)
		c := byte(' ')
		if i < len(text) {
			c = text[i]
		}
		idx := -1
		for j := 0; j < len(ftxChars); j++ {
			if ftxChars[j] == c {
				idx = j
				break
			}
		}
		if idx < 0 {
			t.Fatalf("char %q not in the free-text alphabet", c)
		}
		n.Add(n, big.NewInt(int64(idx)))
	}
	if n.BitLen() > 71 {
		t.Fatalf("free text needs %d bits, max 71", n.BitLen())
	}
	// 77-bit message: 71 payload bits, then n3=0, i3=0.
	msg77 := new(big.Int).Lsh(n, 6)
	var a91 [12]byte
	for i := 0; i < 77; i++ {
		if msg77.Bit(76 - i) != 0 {
			a91[i/8] |= 0x80 >> (i % 8)
		}
	}
	crcMsg := a91
	crcMsg[9] &= 0xF8
	crcMsg[10] = 0
	crc := ftxCRC(crcMsg[:], 96-14)
	a91[9] |= byte(crc >> 11)
	a91[10] = byte(crc >> 3)
	a91[11] |= byte(crc << 5)
	var plain [174]uint8
	for i := 0; i < 91; i++ {
		plain[i] = (a91[i/8] >> (7 - i%8)) & 1
	}
	// Solve A*p = b over GF(2): check m constrains the parity bits it
	// touches (A) and the message-bit parity (b).
	var rows [83]struct {
		a [2]uint64
		b uint8
	}
	for m := 0; m < 83; m++ {
		for i := 0; i < int(ftxNumRows[m]); i++ {
			idx := int(ftxNm[m][i]) - 1
			if idx < 91 {
				rows[m].b ^= plain[idx]
			} else {
				j := idx - 91
				rows[m].a[j/64] ^= 1 << (j % 64)
			}
		}
	}
	// Gauss-Jordan to reduced row echelon form.
	bit := func(r, j int) uint8 { return uint8(rows[r].a[j/64] >> (j % 64)) & 1 }
	for col := 0; col < 83; col++ {
		pivot := -1
		for r := col; r < 83; r++ {
			if bit(r, col) == 1 {
				pivot = r
				break
			}
		}
		if pivot < 0 {
			t.Fatalf("parity submatrix singular at column %d", col)
		}
		rows[col], rows[pivot] = rows[pivot], rows[col]
		for r := 0; r < 83; r++ {
			if r != col && bit(r, col) == 1 {
				rows[r].a[0] ^= rows[col].a[0]
				rows[r].a[1] ^= rows[col].a[1]
				rows[r].b ^= rows[col].b
			}
		}
	}
	for j := 0; j < 83; j++ {
		plain[91+j] = rows[j].b
	}
	if errs := ldpcErrors(&plain); errs != 0 {
		t.Fatalf("encoded codeword fails %d of its own parity checks", errs)
	}
	return plain
}

// ft8Modulate renders the codeword as continuous-phase 8-FSK at 12 kHz:
// 79 symbols of 0.16 s, tone spacing 6.25 Hz, Costas sync at 0/36/72.
func ft8Modulate(cw [174]uint8, f0 float64) []float64 {
	const rate, spb = 12000, 1920
	var tones [79]int
	for i := 0; i < 79; i++ {
		switch {
		case i < 7:
			tones[i] = ft8Costas[i]
		case i >= 36 && i < 43:
			tones[i] = ft8Costas[i-36]
		case i >= 72:
			tones[i] = ft8Costas[i-72]
		default:
			k := i - 7
			if i >= 43 {
				k = i - 14
			}
			tones[i] = ft8Gray[int(cw[k*3])*4+int(cw[k*3+1])*2+int(cw[k*3+2])]
		}
	}
	out := make([]float64, 79*spb)
	phase := 0.0
	for i, tone := range tones {
		freq := f0 + float64(tone)*6.25
		for s := 0; s < spb; s++ {
			out[i*spb+s] = math.Cos(phase)
			phase += 2 * math.Pi * freq / rate
		}
	}
	return out
}

// ft8MixNoise scales unit-variance AWGN so the WSJT-X SNR comes out at
// snrDB, per the derivation in the file header.
func ft8MixNoise(signal []float64, snrDB float64, rng *rand.Rand) []float64 {
	var noiseVar = 0.5 / (math.Pow(10, snrDB/10) * 2500.0 / 6000.0)
	sigma := math.Sqrt(noiseVar)
	out := make([]float64, len(signal))
	for i := range out {
		out[i] = signal[i] + rng.NormFloat64()*sigma
	}
	return out
}

func TestFT8SyntheticSNRLadder(t *testing.T) {
	cw := ft8EncodeFreeText(t, "CQ SDRSTREAM")
	clean := ft8Modulate(cw, 1000)
	// Sanity first: a noiseless decode must recover the exact text and
	// the carrier within a tone spacing.
	if got := FTXDecode(clean, 12000, "FT8"); len(got) != 1 || got[0].Text != "CQ SDRSTREAM" {
		t.Fatalf("noiseless decode = %+v", got)
	} else if got[0].FreqHz < 993 || got[0].FreqHz > 1007 {
		t.Fatalf("carrier at %v Hz, want ~1000", got[0].FreqHz)
	}
	// Measured AWGN behavior (2026-10-23, this test's generator, 3 seeds
	// per point): -18 dB 3/3 decode, -19 dB 2/3, -20 dB 0/3, -21 dB 0/3.
	// Practical threshold is about -19 dB; WSJT-X's published FT8
	// reference is about -21 dB in 2500 Hz AWGN, so this decoder sits
	// roughly 2 dB above the reference on a friendly synthetic channel. Assertions pin only the robust zone: every seed
	// must decode at -14 dB, none may decode at -28 dB. The boundary
	// itself is seed-dependent and stays a measurement, not a gate.
	decodeAt := func(snr float64, seed int64) bool {
		rng := rand.New(rand.NewSource(seed))
		mixed := ft8MixNoise(clean, snr, rng)
		for _, m := range FTXDecode(mixed, 12000, "FT8") {
			if m.Text == "CQ SDRSTREAM" {
				return true
			}
		}
		return false
	}
	for _, snr := range []float64{-18, -19, -20, -21} {
		hits := 0
		for seed := int64(1); seed <= 3; seed++ {
			if decodeAt(snr, seed) {
				hits++
			}
		}
		t.Logf("boundary SNR %+5.1f dB: %d/3 seeds decode", snr, hits)
	}
	for seed := int64(1); seed <= 3; seed++ {
		if !decodeAt(-14, seed) {
			t.Errorf("seed %d: -14 dB decode failed; decoder regressed well inside its measured band", seed)
		}
		if decodeAt(-28, seed) {
			t.Errorf("seed %d: -28 dB decode succeeded; the gate is hallucinating deep-noise messages", seed)
		}
	}
}
