package feeds

import "testing"

func TestSourceTermsSurfaceInStatus(t *testing.T) {
	m := New(10, func(string, ...any) {})
	m.mu.Lock()
	m.source("adsb.lol", "aircraft", 120e9)
	m.source("pskreporter", "digimode", 0)
	m.mu.Unlock()
	got := map[string]SourceStatus{}
	for _, s := range m.Snapshot(1).Sources {
		got[s.Name] = s
	}
	if got["adsb.lol"].License != "ODbL" || got["adsb.lol"].MaxAgeSeconds != 120 {
		t.Fatalf("adsb.lol: %+v", got["adsb.lol"])
	}
	// No licence is invented for sources whose licence is not recorded.
	if got["pskreporter"].License != "" || got["pskreporter"].TermsNotes == "" {
		t.Fatalf("pskreporter: %+v", got["pskreporter"])
	}
}

func TestEverySourceWithMaxAgeHasTermsEntry(t *testing.T) {
	for name := range maxAges {
		if _, ok := TermsFor(name); !ok {
			t.Errorf("source %q has a max age but no terms entry", name)
		}
	}
}
