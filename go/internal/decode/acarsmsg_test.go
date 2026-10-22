package decode

import "testing"

func TestACARSLabelMeaning(t *testing.T) {
	s := InterpretACARS("Q0", "")
	if s.Meaning != "ACARS link test" {
		t.Errorf("meaning = %q", s.Meaning)
	}
	s = InterpretACARS("ZZ", "hello")
	if s.Meaning != "unlisted label" {
		t.Errorf("meaning = %q", s.Meaning)
	}
}

func TestACARSFlightAndOOOI(t *testing.T) {
	// Real VHF ACARS block text captured via airframes.io (acarsdec).
	s := InterpretACARS("17", "210717 KDTW LIRF7\r\n/FN 0206\r\n")
	if s.Flight != "0206" {
		t.Errorf("flight = %q, want 0206", s.Flight)
	}
}

func TestACARSBinary(t *testing.T) {
	s := InterpretACARS("H1", "\x01\x02\x00\x1f\x7f\x05\x06binary payload")
	if s.Kind != "binary" {
		t.Errorf("kind = %q, want binary", s.Kind)
	}
}

func TestACARSEmpty(t *testing.T) {
	s := InterpretACARS("SQ", "   ")
	if s.Kind != "empty" {
		t.Errorf("kind = %q, want empty", s.Kind)
	}
}
