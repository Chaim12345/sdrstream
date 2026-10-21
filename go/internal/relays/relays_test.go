package relays

import "testing"

func TestTable(t *testing.T) {
	table := Table(false)
	if len(table) == 0 {
		t.Fatal("no relays found")
	}
	for _, relay := range table {
		if relay.Sat.UplinkHz == nil {
			t.Errorf("%s has no uplink but was listed as a relay", relay.Sat.Name)
		}
		if relay.Sat.Status != "active" {
			t.Errorf("%s is %s but was included in the active table", relay.Sat.Name, relay.Sat.Status)
		}
	}
}

func TestKindOf(t *testing.T) {
	cases := map[string]Kind{
		"ISS":      FMVoice,
		"AO-73":    Linear,
		"IO-117":   Digipeater,
		"SONATE-2": Digipeater,
	}
	byName := map[string]Kind{}
	for _, relay := range Table(false) {
		byName[relay.Sat.Name] = relay.Kind
	}
	for name, want := range cases {
		if got := byName[name]; got != want {
			t.Errorf("%s: kind = %q, want %q", name, got, want)
		}
	}
}
