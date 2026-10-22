package decode

import (
	"fmt"
	"testing"
)

// aisReviewSentence wraps a body in "!...*CC" with a valid NMEA checksum,
// so probes can vary one field at a time.
func aisReviewSentence(body string) string {
	var sum byte
	for i := 0; i < len(body); i++ {
		sum ^= body[i]
	}
	return fmt.Sprintf("!%s*%02X", body, sum)
}

// TestAISRejectsBadFillBits: the fill count pads the final 6-bit character,
// so only 0..5 is meaningful. Anything else used to shift (or negate) the
// decoded bit count.
func TestAISRejectsBadFillBits(t *testing.T) {
	for _, fill := range []string{"6", "9", "42", "-1", "x"} {
		asm := NewAISAssembler()
		_, _, err := asm.Feed(aisReviewSentence("AIVDM,1,1,,A,15Muq@?P00J;Wp0P4>H>:?0L0," + fill))
		if err == nil {
			t.Errorf("fill=%s accepted, want rejection", fill)
		}
	}
	// The boundary values still decode.
	for _, fill := range []string{"0", "5"} {
		asm := NewAISAssembler()
		if _, _, err := asm.Feed(aisReviewSentence("AIVDM,1,1,,A,15Muq@?P00J;Wp0P4>H>:?0L0," + fill)); err != nil {
			t.Errorf("fill=%s rejected: %v", fill, err)
		}
	}
}

// TestAISRejectsTooManyFragments: AIS never spans more than 9 sentences;
// larger counts used to size the pending-fragment allocations.
func TestAISRejectsTooManyFragments(t *testing.T) {
	for _, nfrag := range []string{"10", "99"} {
		asm := NewAISAssembler()
		_, _, err := asm.Feed(aisReviewSentence("AIVDM," + nfrag + ",1,0,A,15Muq@?,0"))
		if err == nil {
			t.Errorf("nfrag=%s accepted, want rejection", nfrag)
		}
	}
	asm := NewAISAssembler()
	if _, _, err := asm.Feed(aisReviewSentence("AIVDM,9,1,0,A,15Muq@?,0")); err != nil {
		t.Errorf("nfrag=9 rejected: %v", err)
	}
}

// TestAISPendingSequenceCap: fragments whose companions never arrive must
// not grow the assembler state without bound; the oldest pending sequence
// is evicted.
func TestAISPendingSequenceCap(t *testing.T) {
	asm := NewAISAssembler()
	for i := 0; i < maxAISPendingSeqs+50; i++ {
		// Distinct sequence ids, first fragment only: never completes.
		_, _, err := asm.Feed(aisReviewSentence(fmt.Sprintf("AIVDM,2,1,%d,B,15Muq@?P00J;Wp0P4>H>,0", i)))
		if err != nil {
			t.Fatalf("feed %d: %v", i, err)
		}
	}
	if got := len(asm.parts); got > maxAISPendingSeqs {
		t.Errorf("pending sequences = %d, want <= %d", got, maxAISPendingSeqs)
	}
	if len(asm.order) != len(asm.parts) || len(asm.total) != len(asm.parts) || len(asm.rawLog) != len(asm.parts) {
		t.Errorf("bookkeeping maps diverged: order=%d parts=%d total=%d rawLog=%d",
			len(asm.order), len(asm.parts), len(asm.total), len(asm.rawLog))
	}
}

// TestAISBitReaderClamp: the declared bit count can never push reads past
// the de-armoured payload or below zero.
func TestAISBitReaderClamp(t *testing.T) {
	if b := newBitReader([]byte{1, 2}, 100); b.nbits != 12 {
		t.Errorf("nbits = %d, want clamped 12", b.nbits)
	}
	if b := newBitReader([]byte{1}, -3); b.nbits != 0 {
		t.Errorf("nbits = %d, want clamped 0", b.nbits)
	}
	// A one-character payload with five fill bits leaves a single bit;
	// reads beyond it must zero-pad, not panic.
	asm := NewAISAssembler()
	br, _, err := asm.Feed(aisReviewSentence("AIVDM,1,1,,A,0,5"))
	if err != nil {
		t.Fatal(err)
	}
	if br.nbits != 1 {
		t.Errorf("nbits = %d, want 1", br.nbits)
	}
	if _, err := DecodeAIS(br, ""); err == nil {
		t.Error("1-bit message decoded, want too-short error")
	}
}
