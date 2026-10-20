package decode

import "testing"

func TestEASReference(t *testing.T) {
	s, r, err := ReadWAV("../../../tests/data/ref_eas.wav")
	if err != nil {
		t.Fatal(err)
	}
	alerts := EASDecode(s, r)
	if len(alerts) != 2 {
		t.Fatalf("got %d alerts, want 2: %+v", len(alerts), alerts)
	}
	h := alerts[0]
	if h.Origin != "WXR" {
		t.Errorf("origin = %q, want WXR", h.Origin)
	}
	if h.Event != "TOR" {
		t.Errorf("event = %q, want TOR", h.Event)
	}
	if len(h.Locations) != 1 || h.Locations[0] != "039173" {
		t.Errorf("locations = %v, want [039173]", h.Locations)
	}
	if h.Duration != "0045" {
		t.Errorf("duration = %q, want 0045", h.Duration)
	}
	if h.Timestamp != "1421800" {
		t.Errorf("timestamp = %q, want 1421800", h.Timestamp)
	}
	if h.Station != "KCLE/NWS" {
		t.Errorf("station = %q, want KCLE/NWS", h.Station)
	}
	if !alerts[1].End {
		t.Errorf("second alert should be the end-of-message code: %+v", alerts[1])
	}
}
