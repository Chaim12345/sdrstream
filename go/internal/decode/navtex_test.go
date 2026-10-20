package decode

import "testing"

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
