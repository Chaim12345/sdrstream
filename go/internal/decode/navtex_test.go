package decode

import (
	"math"
	"testing"
)

func TestNAVTEXReference(t *testing.T) {
	s, r, err := ReadWAV("../../../tests/data/ref_navtex.wav")
	if err != nil {
		t.Fatal(err)
	}
	msgs := NAVTEXDecode(s, r, 0)
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1: %+v", len(msgs), msgs)
	}
	m := msgs[0]
	if m.Station != "S" {
		t.Errorf("station = %q, want S", m.Station)
	}
	if m.Subject != "A" {
		t.Errorf("subject = %q, want A", m.Subject)
	}
	if m.Serial != "00" {
		t.Errorf("serial = %q, want 00", m.Serial)
	}
	if m.Text != "TEST NAVTEX MESSAGE 123" {
		t.Errorf("text = %q, want TEST NAVTEX MESSAGE 123", m.Text)
	}
}

func TestNAVTEXAutoCenter(t *testing.T) {
	const rate = 22050
	center := 2130.0
	samples := syntheticNAVTEX(rate, center, 99.4, "ZCZC SA00 TEST NAVTEX MESSAGE 123 NNNN")
	if got := navtexCenterHz(samples, rate); got < center-5 || got > center+5 {
		t.Fatalf("auto center = %.0f Hz, want near %.0f Hz", got, center)
	}
	msgs := NAVTEXDecode(samples, rate, 0)
	if len(msgs) != 1 {
		t.Fatalf("auto-center decode got %d messages: %+v", len(msgs), msgs)
	}
	if msgs[0].Station != "S" || msgs[0].Subject != "A" || msgs[0].Serial != "00" || msgs[0].Text != "TEST NAVTEX MESSAGE 123" {
		t.Fatalf("unexpected decoded message: %+v", msgs[0])
	}
}

func syntheticNAVTEX(rate int, center, baud float64, message string) []float64 {
	codes := map[rune]byte{
		'A': 0x47, 'B': 0x72, 'C': 0x1D, 'D': 0x53, 'E': 0x56, 'F': 0x1B,
		'G': 0x35, 'H': 0x69, 'I': 0x4D, 'J': 0x17, 'K': 0x1E, 'L': 0x65,
		'M': 0x39, 'N': 0x59, 'O': 0x71, 'P': 0x2D, 'Q': 0x2E, 'R': 0x55,
		'S': 0x4B, 'T': 0x74, 'U': 0x4E, 'V': 0x3C, 'W': 0x27, 'X': 0x3A,
		'Y': 0x2B, 'Z': 0x63, '0': 0x2D, '1': 0x2E, '2': 0x27, '3': 0x56,
		'4': 0x55, '5': 0x74, '6': 0x2B, '7': 0x4E, '8': 0x4D, '9': 0x71,
		' ': 0x5C,
	}
	symbols := []byte{ccirAlpha, ccirBeta, ccirAlpha, ccirBeta}
	figures := false
	for _, r := range message {
		wantFigures := r >= '0' && r <= '9'
		if wantFigures != figures {
			if wantFigures {
				symbols = append(symbols, ccirFIGS)
			} else {
				symbols = append(symbols, ccirLTRS)
			}
			figures = wantFigures
		}
		if code, ok := codes[r]; ok {
			symbols = append(symbols, code)
		}
	}
	symbols = append(symbols, ccirREP, ccirREP, ccirREP)
	var bits []byte
	for i, symbol := range symbols {
		// DX slot carries the symbol, RX slot the one sent two DX slots
		// earlier, so every repeat lands five slots after the original.
		rx := byte(ccirREP)
		if i >= 2 {
			rx = symbols[i-2]
		}
		for _, code := range []byte{symbol, rx} {
			for bit := 0; bit < 7; bit++ {
				bits = append(bits, (code>>uint(bit))&1)
			}
		}
	}
	samples := make([]float64, 0, int(float64(len(bits))*float64(rate)/baud))
	phase := 0.0
	for i, bit := range bits {
		start := int(float64(i) * float64(rate) / baud)
		end := int(float64(i+1) * float64(rate) / baud)
		frequency := center - navtexShift
		if bit == 1 {
			frequency = center + navtexShift
		}
		step := 2 * 3.141592653589793 * frequency / float64(rate)
		for j := start; j < end; j++ {
			samples = append(samples, 0.6*math.Sin(phase))
			phase += step
		}
	}
	return samples
}

func TestNAVTEXQuietChunksAndDrift(t *testing.T) {
	const rate = 22050
	msg := "ZCZC SA00 TEST NAVTEX MESSAGE 123 NNNN"
	// 50 s of silence (a skipped quiet chunk), then the message at a drifted clock.
	var samples []float64
	samples = append(samples, make([]float64, 50*rate)...)
	samples = append(samples, syntheticNAVTEX(rate, 1700, 99.6, msg)...)
	msgs := NAVTEXDecode(samples, rate, 1700)
	if len(msgs) != 1 || msgs[0].Text != "TEST NAVTEX MESSAGE 123" {
		t.Fatalf("got %+v", msgs)
	}
}

func TestNAVTEXMergeUsesRepeat(t *testing.T) {
	// slot 0 DX = A (corrupted), repeat of A sits in RX slot 5.
	good := byte(0x47)
	chars := []byte{0x00, 0x01, ccirREP, ccirREP, ccirREP, good, ccirREP}
	// With a corrupted DX the RX repeat (phase 0, j=0 -> index 5) must win.
	if got := navtexMerge(chars, 0); len(got) == 0 || got[0] != good {
		t.Fatalf("repeat not used: %v", got)
	}
}

func TestNAVTEXLSBFirst(t *testing.T) {
	// 'A' = 0x47 = 1000111; LSB-first on the wire is 1,1,1,0,0,0,1.
	dec := []byte{1, 1, 1, 0, 0, 0, 1}
	if c := navtexChars(dec, 0, 1, false); len(c) != 1 || c[0] != 0x47 {
		t.Fatalf("got %v", c)
	}
}
